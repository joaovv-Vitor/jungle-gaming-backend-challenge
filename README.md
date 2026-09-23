# Processamento Distribuído de Apostas em Go

Implementação do desafio descrito em `teste tecnico.md`. As decisões de arquitetura e as verificações estão em `ARCHITECTURE.md` e `REQUIREMENTS_AUDIT.md`.

## Estado atual

O projeto inclui bootstrap com Uber Fx, domínio financeiro, PostgreSQL, Keycloak/OIDC e processamento idempotente por HTTP e SQS de `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`. No SQS, inbox, carteira, transação, ledger e outbox são confirmados atomicamente antes da remoção da mensagem. Workers duráveis retomam referências pendentes e publicam a outbox na fila de eventos FIFO.

## Requisitos locais

- Go 1.27;
- Docker com Docker Compose.

## Executar localmente

```sh
docker compose up --build
```

O Compose inicia a aplicação, PostgreSQL, Keycloak, LocalStack e `sqs-gateway`. Para personalizar portas e credenciais locais, copie `.env.example` para `.env` antes de iniciar.

Verifique o processo:

```sh
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
```

O Compose provisiona o realm `wagering`, os clients `internal-service`, `provider-a`, `provider-b` e `wager-api`, além das roles `internal` e `provider`. As credenciais abaixo são exclusivamente locais. O import do Keycloak ocorre apenas na criação do realm; se o volume foi criado antes da inclusão do provider B, recrie o realm em um ambiente descartável ou adicione o client/role de serviço por administração, sem apagar dados de produção.

Obtenha um token interno pelo fluxo `client_credentials`:

```sh
curl -sS -X POST http://localhost:8081/realms/wagering/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode grant_type=client_credentials \
  --data-urlencode client_id=internal-service \
  --data-urlencode client_secret=internal-service-local
```

Copie o `access_token` retornado para `INTERNAL_TOKEN` e crie uma carteira:

```sh
curl -i -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'X-Correlation-ID: wallet-example-1' \
  -d '{"playerId":"10000000-0000-4000-8000-000000000001","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Consultas internas:

```sh
curl -H "Authorization: Bearer $INTERNAL_TOKEN" http://localhost:8080/wallets/WALLET_ID
curl -H "Authorization: Bearer $INTERNAL_TOKEN" 'http://localhost:8080/wallets/WALLET_ID/ledger?limit=50'
curl -X POST -H "Authorization: Bearer $INTERNAL_TOKEN" http://localhost:8080/wallets/WALLET_ID/reconciliation
curl -H "Authorization: Bearer $INTERNAL_TOKEN" http://localhost:8080/metrics
```

O ledger é paginado da versão mais recente para a mais antiga, com limite de 1 a 100 (padrão 50). O cursor devolvido é opaco, vinculado à carteira e deve ser reenviado sem alterações no parâmetro `cursor`. Lançamentos confirmados depois da primeira página não entram nas páginas seguintes dessa travessia; uma nova consulta sem cursor mostra o histórico atualizado.

A reconciliação usa uma visão consistente e compara o saldo armazenado à soma dos créditos menos débitos do ledger, incluindo `OPENING`. Retorna `storedBalance`, `calculatedBalance`, `difference` (armazenado menos calculado), `consistent` e `checkedEntries`, sem alterar a carteira. Somatórios ou diferenças fora do intervalo monetário retornam `500` com `RECONCILIATION_OVERFLOW`; divergências dentro do intervalo aparecem na resposta, no log e na métrica. A rota aceita somente o papel `internal`; UUID inválido retorna `400` e carteira ausente retorna `404`.

### Operações de aposta

Obtenha o token do provider local:

```sh
curl -sS -X POST http://localhost:8081/realms/wagering/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode grant_type=client_credentials \
  --data-urlencode client_id=provider-a \
  --data-urlencode client_secret=provider-a-local
```

Copie o `access_token` para `PROVIDER_TOKEN` e envie uma aposta. O `providerId` precisa coincidir com o claim `provider_id` do token:

```sh
curl -i -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -H 'X-Correlation-ID: wager-example-1' \
  -d '{"providerId":"provider-a","externalTransactionId":"transaction-123","playerId":"PLAYER_ID","walletId":"WALLET_ID","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'
```

Repetir o mesmo corpo e a mesma chave retorna a transação original com `idempotentReplay: true`. Consultas também usam a identidade do provider presente no token:

```sh
curl -H "Authorization: Bearer $PROVIDER_TOKEN" http://localhost:8080/wagering/transactions/TRANSACTION_ID
curl -H "Authorization: Bearer $PROVIDER_TOKEN" http://localhost:8080/providers/provider-a/wagering/transactions/transaction-123
```

`REFUND` e `ROLLBACK` exigem `referenceExternalTransactionId`; `WIN` pode fornecê-lo. Referências ainda não recebidas retornam `202` com estado `PENDING_REFERENCE` e ficam agendadas de forma durável. O worker reavalia a referência sob o lock da carteira, com backoff entre 1 segundo e 5 minutos e limite de espera de 24 horas. Uma referência recebida posteriormente pode concluir a transação; uma referência rejeitada ou incompatível rejeita a dependente. Se continuar ausente ou pendente após o prazo, a transação é rejeitada com `REFERENCE_NOT_FOUND`. Consulte a transação pelo `transactionId` para obter o estado atualizado; repetir o envio idempotente também devolve esse estado. A agenda e o lease ficam no PostgreSQL, permitindo retomada por outra instância após falha.

O worker usa `APP_REFERENCE_WORKERS` (padrão `2`), `APP_REFERENCE_POLL_INTERVAL` (`500ms`), `APP_REFERENCE_PROCESSING_TIMEOUT` (`10s`) e `APP_REFERENCE_LEASE` (`30s`). Configure o lease acima do timeout de processamento e mantenha capacidade de conexões PostgreSQL para os workers e demais consumidores.

### Códigos de falha da transação

`failureCode` aparece no resultado persistido de uma transação `REJECTED` e nas consultas posteriores. Uma rejeição é terminal: repetir a mesma identidade devolve o resultado anterior, sem nova movimentação. Para uma tentativa corrigida, use uma nova identidade externa e uma nova chave de idempotência. `PENDING_REFERENCE` não possui `failureCode`: aguarde e consulte o `transactionId`. Erros de entrada ou conflito que ocorrem antes da persistência usam o código HTTP e o campo `error` descritos abaixo, sem `failureCode`.

| failureCode | Situação que causa o código | Estado resultante | Definitivo ou temporário | Ação esperada do cliente |
| --- | --- | --- | --- | --- |
| `BET_INSUFFICIENT_FUNDS` | Aposta excede o saldo disponível. | `REJECTED` | Definitivo para esta transação. | Não repetir a mesma operação; consultar saldo e, se cabível, criar nova aposta com valor adequado. |
| `REVERSAL_INSUFFICIENT_FUNDS` | `ROLLBACK` precisa debitar mais que o saldo disponível. | `REJECTED` | Definitivo para esta transação. | Não repetir a mesma identidade; consultar saldo e tratar a reversão com o provedor. |
| `REFERENCE_NOT_FOUND` | Referência de `WIN`, `REFUND` ou `ROLLBACK` segue ausente ou pendente após o TTL de 24 horas. | `REJECTED` | Definitivo após o prazo. | Consultar o resultado e não repetir esta operação; se a referência chegar depois, decidir uma nova tentativa com nova identidade. |
| `REFERENCE_NOT_PROCESSED` | Referência encontrada terminou em `REJECTED` ou `FAILED`. | `REJECTED` | Definitivo para esta transação. | Consultar a referência e corrigir o fluxo de origem; não repetir a mesma operação. |
| `REFERENCE_MISMATCH` | Referência processada diverge em provedor, jogador, carteira, rodada ou moeda. | `REJECTED` | Definitivo para esta transação. | Corrigir os identificadores ou a moeda e criar uma nova operação, se cabível. |
| `REFERENCE_TYPE_NOT_ALLOWED` | Tipo da referência não é permitido para a operação (`WIN`, `REFUND` ou `ROLLBACK`). | `REJECTED` | Definitivo para esta transação. | Corrigir o tipo ou a referência; não repetir a mesma identidade. |
| `ALREADY_REVERSED` | Outra reversão direta da mesma referência já foi processada. | `REJECTED` | Definitivo. | Consultar a reversão existente e não enviar outra com o mesmo propósito. |
| `CURRENCY_MISMATCH` | Código definido para incompatibilidade de moeda; no fluxo atual, divergência entre carteira e operação retorna `422 WALLET_MISMATCH` antes da persistência. | Não emitido atualmente. | Corrigível antes da criação. | Corrigir carteira ou moeda da requisição; não aguardar retry da operação inválida. |
| `INVALID_OPERATION_AMOUNT` | Valor de `REFUND` ou `ROLLBACK` difere do valor da referência. | `REJECTED` | Definitivo para esta transação. | Corrigir o valor e criar nova operação; não repetir a mesma identidade. |
| `WALLET_PLAYER_MISMATCH` | Código definido para divergência entre carteira e jogador; o fluxo atual retorna `422 WALLET_MISMATCH` antes da persistência. | Não emitido atualmente. | Corrigível antes da criação. | Corrigir `walletId` ou `playerId` na requisição. |
| `INVALID_REFERENCE` | Código definido para referência inválida; a validação atual rejeita essa entrada antes da persistência com `400 INVALID_REQUEST`. | Não emitido atualmente. | Corrigível antes da criação. | Corrigir `referenceExternalTransactionId` e a requisição. |
| `MONEY_OVERFLOW` | Crédito ou débito produziria valor fora do intervalo monetário suportado. | `REJECTED` | Definitivo para esta transação. | Não repetir a mesma operação; ajustar valor ou saldo por um fluxo autorizado antes de uma nova tentativa. |
| `INFRASTRUCTURE_PERMANENT_FAILURE` | Código aceito por `Transaction.Fail()` para falha de infraestrutura comprovadamente permanente; o fluxo operacional atual não classifica falhas transitórias assim. | `FAILED` apenas na transição de domínio; não emitido em produção. | Definitivo se algum fluxo futuro o persistir. | Consultar a transação e acionar investigação operacional; não repetir automaticamente uma operação `FAILED`. |

Indisponibilidade transitória de PostgreSQL ou SQS não gera `FAILED` nem um desses `failureCode`: HTTP devolve `503 TRANSIENT_FAILURE` e o cliente deve repetir **a mesma** identidade; o consumidor SQS reentrega com backoff. Isso preserva idempotência quando o resultado do commit é incerto.

### Operações via SQS

O Compose provisiona `wager-transactions.fifo`, `wager-transactions-dlq.fifo` e `wager-events.fifo` no LocalStack. A fila de entrada usa long polling de 20 segundos, visibility de 60 segundos e redrive após cinco recebimentos. Envie mensagens usando `walletId` como `MessageGroupId` e uma identidade de transporte estável como `MessageDeduplicationId`:

```sh
docker compose exec -T \
  -e AWS_ACCESS_KEY_ID=wager-producer-local \
  -e AWS_SECRET_ACCESS_KEY=wager-producer-local-secret \
  localstack awslocal --endpoint-url http://sqs-gateway:4566 sqs send-message \
  --queue-url http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/wager-transactions.fifo \
  --message-group-id WALLET_ID \
  --message-deduplication-id msg-123 \
  --message-body '{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"transaction-123","idempotencyKey":"provider-a:transaction-123","playerId":"PLAYER_ID","walletId":"WALLET_ID","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}'
```

O `messageId` do envelope identifica a inbox. Reentregas com o mesmo conteúdo são confirmadas sem reaplicar o efeito; o mesmo `messageId` com conteúdo diferente permanece na fila para redrive. Um `messageId` novo ainda é deduplicado pelas identidades financeiras compartilhadas com o HTTP.

Falhas transitórias de processamento ajustam a visibility da mensagem para `5, 10, 20, 40, 80` segundos nas cinco primeiras entregas, limitados a 300 segundos se a política de redrive for ampliada. Se o ajuste falhar, a visibility original continua valendo e a inbox/idempotência mantêm a reentrega segura. Envelopes inválidos e conflitos permanentes não recebem esse backoff e seguem a política de redrive da fila; cancelamento no shutdown libera a visibility para retomada. A fila local envia à DLQ após cinco recebimentos.

A fila compartilhada pressupõe um produtor interno confiável; `providerId` no JSON não é uma credencial. No Compose principal, somente `sqs-gateway` publica a porta SQS; o LocalStack fica em uma rede interna. O gateway verifica a assinatura SigV4 e aplica os templates de `deploy/aws/` às filas efetivamente usadas: `wager-producer-local` pode enviar à entrada; `wager-app-local` pode consumir a entrada e publicar eventos. Operações por URL são autorizadas pelo `QueueUrl`, enquanto `GetQueueUrl` e `CreateQueue` usam `QueueName`; identificadores extras ou conflitantes são rejeitados antes do proxy. Credenciais desconhecidas, segredo incorreto e ações fora da política recebem `AccessDenied` antes do encaminhamento. A identidade `test/test` serve somente às filas isoladas criadas pelos testes e não tem acesso às três filas da aplicação. `wager-test-app-local` permite que um processo de integração consuma uma fila isolada e acesse a saída/DLQ com a política do app, mas não concede acesso à entrada financeira principal. Esses pares são exemplos locais; defina segredos próprios se expuser a porta fora de uma máquina de desenvolvimento. `TestMainSQSRejectsUnauthorizedFinancialMessage` comprova publicação permitida e ausência de alterações em saldo, ledger, transações e inbox após tentativas negadas, inclusive com `QueueName` e `QueueUrl` conflitantes. `TestIAMPolicyTemplatesUseLeastPrivilegeQueueActions` verifica as ações e recursos dos templates. O gateway local existe porque o [enforcement de IAM no LocalStack exige a edição Pro](https://docs.localstack.cloud/aws/developer-tools/security-testing/iam-policy-enforcement/).

Em AWS, configure `APP_SQS_REGION` para a região das filas e deixe `APP_SQS_ENDPOINT`, `APP_SQS_ACCESS_KEY_ID` e `APP_SQS_SECRET_ACCESS_KEY` vazios. O adaptador usa então o endpoint normal do SQS e a [cadeia padrão de credenciais do AWS SDK for Go v2](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html), incluindo a role IAM atribuída ao processo. Associe os templates às roles correspondentes na AWS. O gateway é exclusivo do Compose local. Se fornecer credenciais estáticas, informe acesso e segredo juntos.

Verificação opcional de acesso efetivo em AWS: após provisionar as três filas FIFO vazias (`wager-transactions.fifo`, `wager-transactions-dlq.fifo`, `wager-events.fifo`) e associar as políticas às roles do app e do produtor, revise queue policies e demais controles da conta. Configure três perfis de credenciais AWS que assumam essas identidades e uma terceira sem acesso às filas, então execute:

```sh
IAM_TEST_REGION=us-east-1 \
IAM_TEST_INPUT_QUEUE_URL=https://sqs.us-east-1.amazonaws.com/ACCOUNT_ID/wager-transactions.fifo \
IAM_TEST_DLQ_QUEUE_URL=https://sqs.us-east-1.amazonaws.com/ACCOUNT_ID/wager-transactions-dlq.fifo \
IAM_TEST_OUTPUT_QUEUE_URL=https://sqs.us-east-1.amazonaws.com/ACCOUNT_ID/wager-events.fifo \
IAM_TEST_APP_PROFILE=wager-app-test \
IAM_TEST_PRODUCER_PROFILE=wager-producer-test \
IAM_TEST_UNTRUSTED_PROFILE=wager-untrusted-test \
IAM_TEST_ISOLATED_QUEUES=yes \
go test -race -tags=integration -run '^TestAWSIAMQueuePermissions$' -count=1 -v ./internal/adapters/sqs
```

O teste confirma identidades IAM distintas, permite o envio do produtor à entrada, o recebimento, a alteração de visibilidade e o delete pelo app, a leitura das filas pelo app e a publicação do app na saída. Exige negação real para envios, consumo, delete, alteração de visibilidade e leitura fora dessas permissões. Ele envia uma mensagem a cada uma das filas de entrada e saída: apaga a da entrada, mas a da saída permanece até que a fila descartável seja removida. Sem as variáveis, o teste registra `SKIP`; isso não comprova a política. Falha de rede ou fila inexistente também reprova a verificação. Não configure endpoints personalizados de SQS ou STS nos perfis usados pelo teste.

Eventos financeiros são gravados na outbox no mesmo commit da operação e publicados depois em `wager-events.fifo`. O envio é *at-least-once*: se houver queda após o envio e antes da confirmação no banco, o mesmo `eventId` pode ser publicado novamente. Consumidores da fila de eventos devem deduplicar por `eventId`; a deduplicação temporária da FIFO não substitui essa regra. `MessageGroupId` usa a carteira, e `MessageDeduplicationId` usa o `eventId`. Publicações de workers distintos podem chegar fora da ordem dos commits; `walletVersion` permite identificar lacunas nos eventos de saldo.

O publisher usa `APP_SQS_OUTPUT_QUEUE` (padrão `wager-events.fifo`), `APP_OUTBOX_WORKERS` (`2`), `APP_OUTBOX_POLL_INTERVAL` (`500ms`), `APP_OUTBOX_PROCESSING_TIMEOUT` (`10s`) e `APP_OUTBOX_LEASE` (`30s`). Falhas mantêm o evento na outbox, com `attempts`, `next_attempt_at` e `last_error` consultáveis no PostgreSQL. `last_error` armazena uma categoria segura (`publish_timeout`, `publish_network`, `publish_database` ou `publish_unexpected`), sem a mensagem bruta da dependência; não há descarte após um número fixo de tentativas. Configure o lease acima do timeout de processamento.

O endpoint `/metrics` exige token `internal`. As métricas cobrem resultados financeiros após commit, replays, rejeições, latência, entregas SQS, tamanho aproximado da DLQ, tentativas de referências, backlog e idade da outbox, publicações e republicações, falhas de readiness, divergências de reconciliação e duração do shutdown. Os labels usam categorias limitadas; IDs financeiros e de mensagens ficam apenas em logs estruturados. A fila de saída também participa do readiness. A DLQ monitorada usa `APP_SQS_DLQ_QUEUE` (padrão `wager-transactions-dlq.fifo`).

| Situação | HTTP | Código/estado |
| --- | ---: | --- |
| Operação processada ou rejeitada por regra financeira | 200 | `PROCESSED` ou `REJECTED` com `failureCode` |
| Referência ainda ausente | 202 | `PENDING_REFERENCE` |
| Token ausente, inválido ou sem role | 401/403 | `UNAUTHORIZED` ou `FORBIDDEN` |
| Provider do corpo/caminho diferente do token | 403 | `PROVIDER_MISMATCH` |
| JSON, dinheiro ou chave ausente inválidos | 400 | `INVALID_REQUEST`, `INVALID_MONEY` ou `IDEMPOTENCY_KEY_REQUIRED` |
| Reuso conflitante de chave ou ID externo | 409 | `IDEMPOTENCY_CONFLICT` ou `EXTERNAL_TRANSACTION_CONFLICT` |
| Carteira ou transação inexistente | 404 | `WALLET_NOT_FOUND` ou `TRANSACTION_NOT_FOUND` |
| Falha concorrente ou de dependência transitória | 503 | `TRANSIENT_FAILURE`; repetir com a mesma identidade |

Os endpoints que recebem JSON aceitam até 1 MiB por corpo; conteúdo acima desse limite retorna `400 INVALID_REQUEST`.

### Respostas dos endpoints de carteira

| Situação | HTTP | Código |
| --- | ---: | --- |
| Token ausente, inválido ou expirado | 401 | `UNAUTHORIZED` |
| Identidade sem a role interna | 403 | `FORBIDDEN` |
| JSON, dinheiro, UUID ou paginação inválidos | 400 | `INVALID_REQUEST`, `INVALID_MONEY`, `INVALID_WALLET_ID` ou `INVALID_PAGINATION` |
| Carteira inexistente | 404 | `WALLET_NOT_FOUND` |
| Jogador e moeda já possuem carteira | 409 | `WALLET_ALREADY_EXISTS` |
| Banco temporariamente indisponível | 503 | `TRANSIENT_FAILURE` |
| Falha inesperada | 500 | `INTERNAL_ERROR` |

## PostgreSQL e migrations

O Compose cria uma role administrativa para migrations e uma role limitada para a aplicação. Em um volume novo, todas as migrations `*.up.sql` são aplicadas automaticamente em ordem lexical:

```sh
docker compose up -d postgres
docker compose ps postgres
```

Aplicação manual em um banco vazio:

```sh
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000001_initial.up.sql
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000002_financial_semantics.up.sql
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000003_pending_reference_deadline.up.sql
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000004_wallet_ledger_continuity.up.sql
```

Em um volume já existente na versão 2, aplique as migrations 3 e 4 antes de iniciar a versão nova do app; em um volume na versão 3, aplique apenas a migration 4. A migration 3 antecipa para o prazo de expiração qualquer referência pendente agendada depois dele. A migration 4 recusa dados históricos cujo saldo não corresponde ao ledger ou cujos lançamentos não formam uma sequência contínua; resolva a divergência antes de reaplicá-la. Consulte `schema_migrations` para confirmar a versão aplicada.

Reversão em ordem inversa; a última etapa remove permanentemente todas as tabelas e seus dados:

```sh
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000004_wallet_ledger_continuity.down.sql
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000003_pending_reference_deadline.down.sql
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000002_financial_semantics.down.sql
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000001_initial.down.sql
```

As credenciais de `.env.example` são apenas para o ambiente local.

Para inspecionar os planos críticos sem executar os workers, use `docker compose exec -T postgres psql -U wager_admin -d wagering < scripts/explain_worker_queries.sql`. Em dados locais pequenos, o planner usa `wager_pending_reference_work` e `outbox_pending_work` com `Index Cond` no prazo, `ledger_wallet_page` para paginação e a chave primária da inbox. Para repetir a análise com 100 mil registros sintéticos em cada tabela, execute `docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U wager_admin -d wagering < scripts/explain_representative_queries.sql`. Esse segundo script cria apenas tabelas temporárias, aplica `ANALYZE`/`VACUUM` nelas e compara claims com 5 mil itens vencidos sob lease ativo antes e depois de alinhar a próxima elegibilidade ao prazo do lease. As tabelas somem ao fechar a sessão. Os tempos são diagnósticos locais; uma medição de capacidade exige distribuição de dados e carga reais do ambiente alvo.

O prazo global de parada do Fx é calculado a partir dos limites HTTP, SQS, referência e outbox (95 segundos com os defaults). No Compose, `APP_STOP_GRACE_PERIOD` é 100 segundos por padrão; mantenha-o maior que o orçamento global ao personalizar timeouts. O SQS usa processamento de 20 segundos, visibility de 60 segundos e janela de shutdown de 30 segundos; referências e outbox usam processamento de 10 segundos e lease de 30 segundos.

Deadlocks (`40P01`) e falhas de serialização (`40001`) repetem a transação financeira inteira até três tentativas, com espera curta e jitter. Outros erros de infraestrutura e commits de resultado desconhecido não são repetidos automaticamente; o cliente deve reenviar a mesma identidade quando receber `503 TRANSIENT_FAILURE`.

## Verificações

```sh
go test ./...
go test -race ./...
go vet ./...
go test -tags=integration ./internal/adapters/postgres
go test -tags=integration ./internal/adapters/auth
go test -tags=integration -run TestKeycloakIssuedTokenIsRejectedAfterExpiry -v ./internal/adapters/auth
go test -tags=integration ./internal/adapters/sqs
go test -tags=integration -run TestTransientFailureDefersRedeliveryInSQS -v ./internal/adapters/sqs
go test -tags=integration -run TestMigrationsUpDownUpInDisposableSchema -v ./internal/adapters/postgres
go test -tags=integration -run 'Test(RuntimeRoleRejectsLedgerThatStartsFromInventedBalance|ReadCommittedRetryRecoversFromRealDeadlock)$' -v ./internal/adapters/postgres
go test -tags=integration -run TestFourEventContractsReachSQSFromFinancialOperations -v ./internal/adapters/sqs
go test -tags=integration -run TestThreeProcessesSerializeAndReplayAfterRestart -v ./internal/bootstrap
go test -tags=integration -run TestLedgerCursorRemainsStableWhileNewMovementsCommit -v ./internal/bootstrap
go test -tags=integration -run TestSIGTERMReleasesInFlightSQSMessageForAnotherProcess -v ./internal/bootstrap
go test -tags=integration -run TestPendingReferencesResolveAndExpireAfterFullProcessRestart -v ./internal/bootstrap
go test -tags=integration -run TestTemporaryPostgresAndSQSOutagesRecover -v ./internal/bootstrap
```

Os testes com tag `integration` exigem os serviços correspondentes ativos pelo Compose. Para evitar disputa pelas fixtures de referência e outbox, pare apenas o app durante as suítes PostgreSQL/SQS e o teste de restart de referências (`docker compose stop app`) e religue-o depois (`docker compose start app`). O teste de expiração usa as credenciais administrativas locais (`KEYCLOAK_ADMIN` e `KEYCLOAK_ADMIN_PASSWORD`, com os padrões do Compose), cria um realm temporário e o remove ao terminar. O teste de migrations usa a role administrativa, cria um schema exclusivo e o remove ao terminar; configure `APP_DATABASE_ADMIN_URL` se necessário. O teste dos quatro contratos de evento cria uma carteira e fila FIFO isoladas e compara as mensagens aos snapshots persistidos na outbox. O teste multiprocesso `internal/bootstrap` constrói o binário e inicia três processos adicionais em portas livres; pode rodar com o app do Compose ativo e registra seus PIDs com `-v`. Ele exercita concorrência, lock entre carteiras e replay depois de reiniciar todas as três instâncias. O teste de referências pendentes encerra o processo inicial, inicia outro, resolve uma referência tardia e rejeita outra vencida, verificando outbox e replay. O teste de `SIGTERM` no SQS cria e remove uma fila FIFO isolada, bloqueia o consumo no PostgreSQL e comprova liberação antecipada da visibility e retomada por outro processo. O teste de indisponibilidade usa uma instância própria e proxies de falha locais para PostgreSQL/SQS; não para os containers, verifica readiness/liveness e a publicação da outbox após recuperação. A suíte SQS cria filas FIFO isoladas, valida redrive e três consumidores concorrentes contra LocalStack e PostgreSQL reais e remove as filas ao final. O cenário de divergência da reconciliação usa a role administrativa local para alterar somente a carteira criada pelo teste e restaura seu saldo; em outra configuração, informe `APP_DATABASE_ADMIN_URL`.

Para ensaiar um volume novo sem apagar o banco principal, use outro nome de projeto e portas livres. O procedimento abaixo valida build, readiness, migrations, filas e testes na mesma stack protegida. Pare o app do Compose antes da suíte integrada, que inicia seus próprios processos.

```sh
export KEYCLOAK_PORT=18081 POSTGRES_PORT=15432 LOCALSTACK_PORT=14566 APP_HTTP_PORT=18080
docker compose -p wager-clean-smoke up -d --build --wait
curl -f http://localhost:18080/health/ready
docker compose -p wager-clean-smoke exec -T postgres \
  psql -U wager_admin -d wagering -Atc "SELECT string_agg(version::text, ',' ORDER BY version) FROM schema_migrations"
docker compose -p wager-clean-smoke exec -T -e AWS_ACCESS_KEY_ID=wager-producer-local \
  -e AWS_SECRET_ACCESS_KEY=wager-producer-local-secret localstack \
  awslocal --endpoint-url http://sqs-gateway:4566 sqs get-queue-url --queue-name wager-transactions.fifo
go test -race -count=1 ./...
go vet ./...
export APP_DATABASE_URL='postgres://wager_app:wager_app_local@localhost:15432/wagering?sslmode=disable'
export APP_DATABASE_ADMIN_URL='postgres://wager_admin:wager_admin_local@localhost:15432/wagering?sslmode=disable'
export APP_OIDC_ISSUER='http://localhost:18081/realms/wagering'
export APP_OIDC_JWKS_URL='http://localhost:18081/realms/wagering/protocol/openid-connect/certs'
export APP_SQS_ENDPOINT='http://localhost:14566'
export APP_SQS_ACCESS_KEY_ID=wager-app-local APP_SQS_SECRET_ACCESS_KEY=wager-app-local-secret
docker compose -p wager-clean-smoke stop app
go test -race -tags=integration -p 1 -count=1 \
  ./internal/adapters/postgres ./internal/adapters/auth ./internal/adapters/sqs ./internal/bootstrap
docker compose -p wager-clean-smoke down -v
```

Troque as portas se estiverem ocupadas. Execute `down -v` somente com o nome do projeto descartável; ele remove o volume desse projeto. Para conferir o provider B, use o exemplo de token acima com `client_id=provider-b`, `client_secret=provider-b-local` e porta `18081`.
