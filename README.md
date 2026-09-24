# Processamento Distribuído de Apostas em Go

Implementação do desafio descrito em [teste tecnico.md](teste%20tecnico.md). Decisões e limites estão em [ARCHITECTURE.md](ARCHITECTURE.md).

## Visão geral

O serviço Go usa Uber Fx, `net/http`, PostgreSQL, Keycloak/OIDC e SQS no LocalStack para processar `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK` por HTTP ou mensageria. Uma carteira e seu ledger preservam o resultado financeiro; inbox e idempotência evitam efeitos repetidos, enquanto workers retomam referências pendentes e publicam eventos da outbox após o commit.

## Requisitos locais

- Docker com Docker Compose e portas locais livres: `8080` (API), `8081` (Keycloak), `5432` (PostgreSQL) e `4566` (SQS Gateway);
- Go 1.27 para executar os comandos de teste no host. `curl` é usado nos exemplos HTTP.

## Executar localmente

```sh
docker compose up --build
```

O Compose inicia aplicação, PostgreSQL, Keycloak, LocalStack e `sqs-gateway`. O banco aplica as migrations e o LocalStack cria as três filas automaticamente em volumes novos. Aguarde os serviços ficarem saudáveis em `docker compose ps`; `/health/live` indica processo ativo e `/health/ready` confirma PostgreSQL, Keycloak e as filas. Para personalizar portas e credenciais exclusivamente locais, copie `.env.example` para `.env` antes de iniciar. O Compose já fornece padrões locais; não é necessário criar `.env` para o primeiro uso nem fornecer segredos reais.

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

Copie o `access_token` retornado e defina `INTERNAL_TOKEN` (sem as aspas do JSON). Crie uma carteira:

```sh
export INTERNAL_TOKEN='COLE_O_ACCESS_TOKEN_INTERNO'
curl -i -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'X-Correlation-ID: wallet-example-1' \
  -d '{"playerId":"10000000-0000-4000-8000-000000000001","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Copie o `id` da resposta para `WALLET_ID` e use a mesma carteira nos próximos exemplos:

```sh
export WALLET_ID='COLE_O_ID_DA_CARTEIRA'
curl -H "Authorization: Bearer $INTERNAL_TOKEN" "http://localhost:8080/wallets/$WALLET_ID"
curl -H "Authorization: Bearer $INTERNAL_TOKEN" "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50"
curl -X POST -H "Authorization: Bearer $INTERNAL_TOKEN" "http://localhost:8080/wallets/$WALLET_ID/reconciliation"
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

Copie o `access_token` para `PROVIDER_TOKEN` e envie uma aposta. O `providerId` precisa coincidir com o claim `provider_id` do token; `PLAYER_ID` é o mesmo UUID usado na abertura:

```sh
export PROVIDER_TOKEN='COLE_O_ACCESS_TOKEN_DO_PROVIDER'
export PLAYER_ID='10000000-0000-4000-8000-000000000001'
curl -i -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -H 'X-Correlation-ID: wager-example-1' \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"transaction-123\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-987\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
```

Repetir o mesmo corpo e a mesma chave retorna a transação original com `idempotentReplay: true`. Copie `transactionId` para `TRANSACTION_ID`; as consultas usam a identidade do provider presente no token:

```sh
export TRANSACTION_ID='COLE_O_ID_DA_TRANSACAO'
curl -H "Authorization: Bearer $PROVIDER_TOKEN" "http://localhost:8080/wagering/transactions/$TRANSACTION_ID"
curl -H "Authorization: Bearer $PROVIDER_TOKEN" http://localhost:8080/providers/provider-a/wagering/transactions/transaction-123
```

Para os demais tipos, reutilize a chamada `POST` acima com **novo** `externalTransactionId` e `Idempotency-Key` para cada operação; mantenha `providerId`, `playerId`, `walletId`, `roundId` e `gameId`:

| Tipo | `money.amount` de exemplo | Diferença no corpo |
| --- | --- | --- |
| `WIN` | `10.00` | `referenceExternalTransactionId` é opcional; se usado, pode ser `transaction-123`. |
| `LOSS` | `0.00` | Não movimenta saldo nem cria lançamento no ledger. |
| `REFUND` | `25.00` | Exige `"referenceExternalTransactionId":"transaction-123"` para devolver integralmente a `BET` do exemplo. |
| `ROLLBACK` | `25.00` | Exige referência a uma `BET`, `WIN` ou `REFUND` processada e inverte sua movimentação. Use uma operação de origem ainda não revertida. |

`REFUND` e `ROLLBACK` exigem `referenceExternalTransactionId`; `WIN` pode fornecê-lo. Referências ainda não recebidas retornam `202` com estado `PENDING_REFERENCE` e ficam agendadas de forma durável. O worker reavalia a referência sob o lock da carteira, com backoff entre 1 segundo e 5 minutos e limite de espera de 24 horas. Uma referência recebida posteriormente pode concluir a transação; uma referência rejeitada ou incompatível rejeita a dependente. Se continuar ausente ou pendente após o prazo, a transação é rejeitada com `REFERENCE_NOT_FOUND`. Consulte a transação pelo `transactionId` para obter o estado atualizado; repetir o envio idempotente também devolve esse estado. A agenda e o lease ficam no PostgreSQL, permitindo retomada por outra instância após falha.

O header `Idempotency-Key` é obrigatório. Mesmo payload e mesma chave produzem replay do saldo histórico; reutilizar a chave com payload diferente retorna `409 IDEMPOTENCY_CONFLICT`. O par `(providerId, externalTransactionId)` também é único: outra chave não reaplica a operação. HTTP e SQS compartilham essas identidades e o hash SHA-256 do JSON canônico de negócio, após normalização de `money`, sem headers ou metadados de transporte.

O worker usa `APP_REFERENCE_WORKERS` (padrão `2`), `APP_REFERENCE_POLL_INTERVAL` (`500ms`), `APP_REFERENCE_PROCESSING_TIMEOUT` (`10s`) e `APP_REFERENCE_LEASE` (`30s`). Configure o lease acima do timeout de processamento e mantenha capacidade de conexões PostgreSQL para os workers e demais consumidores.

### Códigos de falha da transação

`failureCode` aparece no resultado persistido de uma transação `REJECTED` e nas consultas posteriores. Uma rejeição é terminal: repetir a mesma identidade devolve o resultado anterior, sem nova movimentação. Para uma tentativa corrigida, use uma nova identidade externa e uma nova chave de idempotência. `PENDING_REFERENCE` não possui `failureCode`: aguarde e consulte o `transactionId`. Erros de entrada ou conflito antes da persistência usam o código HTTP e corpo `{"code":"INVALID_REQUEST"}` (com o código específico da tabela), sem `failureCode`.

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
  --message-group-id "$WALLET_ID" \
  --message-deduplication-id msg-123 \
  --message-body "{\"messageId\":\"msg-123\",\"type\":\"WagerTransactionRequested\",\"occurredAt\":\"2026-09-08T12:00:00Z\",\"data\":{\"providerId\":\"provider-a\",\"externalTransactionId\":\"transaction-123\",\"idempotencyKey\":\"provider-a:transaction-123\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-987\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}}"
```

Esse envio reutiliza a identidade da `BET` HTTP acima para demonstrar replay entre transportes. Para uma operação SQS nova, altere juntos `messageId`, `MessageDeduplicationId`, `externalTransactionId` e `idempotencyKey`. O `messageId` do envelope identifica a inbox. Reentregas com o mesmo conteúdo são confirmadas sem reaplicar o efeito; o mesmo `messageId` com conteúdo diferente permanece na fila para redrive. Um `messageId` novo ainda é deduplicado pelas identidades financeiras compartilhadas com o HTTP.

Falhas transitórias de processamento ajustam a visibility da mensagem para `5, 10, 20, 40, 80` segundos nas cinco primeiras entregas, limitados a 300 segundos se a política de redrive for ampliada. Se o ajuste falhar, a visibility original continua valendo e a inbox/idempotência mantêm a reentrega segura. Envelopes inválidos e conflitos permanentes não recebem esse backoff e seguem a política de redrive da fila; cancelamento no shutdown libera a visibility para retomada. A fila local envia à DLQ após cinco recebimentos.

A fila compartilhada pressupõe um produtor interno confiável; `providerId` no JSON não é uma credencial. No Compose principal, somente `sqs-gateway` publica a porta SQS; o LocalStack fica em uma rede interna. O gateway verifica a assinatura SigV4 e aplica os templates de `deploy/aws/` às filas efetivamente usadas: `wager-producer-local` pode enviar à entrada; `wager-app-local` pode consumir a entrada e publicar eventos. Operações por URL são autorizadas pelo `QueueUrl`, enquanto `GetQueueUrl` e `CreateQueue` usam `QueueName`; identificadores extras ou conflitantes são rejeitados antes do proxy. Credenciais desconhecidas, segredo incorreto e ações fora da política recebem `AccessDenied` antes do encaminhamento. A identidade `test/test` serve somente às filas isoladas criadas pelos testes e não tem acesso às três filas da aplicação. `wager-test-app-local` permite que um processo de integração consuma uma fila isolada e acesse a saída/DLQ com a política do app, mas não concede acesso à entrada financeira principal. Esses pares são exemplos locais; defina segredos próprios se expuser a porta fora de uma máquina de desenvolvimento. `TestMainSQSRejectsUnauthorizedFinancialMessage` comprova publicação permitida e ausência de alterações em saldo, ledger, transações e inbox após tentativas negadas, inclusive com `QueueName` e `QueueUrl` conflitantes. `TestIAMPolicyTemplatesUseLeastPrivilegeQueueActions` verifica as ações e recursos dos templates. O gateway local existe porque o [enforcement de IAM não fica ativo por padrão no LocalStack](https://docs.localstack.cloud/aws/developer-tools/security-testing/iam-policy-enforcement/); sua política cobre apenas as operações deste desafio.

Em AWS, configure `APP_SQS_REGION` para a região das filas e deixe `APP_SQS_ENDPOINT`, `APP_SQS_ACCESS_KEY_ID` e `APP_SQS_SECRET_ACCESS_KEY` vazios. O adaptador usa então o endpoint normal do SQS e a [cadeia padrão de credenciais do AWS SDK for Go v2](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html), incluindo a role IAM atribuída ao processo. Associe os templates às roles correspondentes na AWS. O gateway é exclusivo do Compose local. Se fornecer credenciais estáticas, informe acesso e segredo juntos.

Eventos financeiros são gravados na outbox no mesmo commit da operação e publicados depois em `wager-events.fifo`. O envio é *at-least-once*: se houver queda após o envio e antes da confirmação no banco, o mesmo `eventId` pode ser publicado novamente. Consumidores da fila de eventos devem deduplicar por `eventId`; a deduplicação temporária da FIFO não substitui essa regra. `MessageGroupId` usa a carteira, e `MessageDeduplicationId` usa o `eventId`. Publicações de workers distintos podem chegar fora da ordem dos commits; `walletVersion` permite identificar lacunas nos eventos de saldo.

O envelope de saída (versão `1`) contém `eventId`, `eventType`, `aggregateId`, `correlationId`, `occurredAt`, `version`, `data` e `causationId` quando aplicável. Os tipos são `WagerTransactionProcessed` (inclui `LOSS`), `WagerTransactionRejected` (inclui `failureCode`), `WagerTransactionPendingReference` e `WalletBalanceChanged` (inclui direção, valor, saldos antes/depois e `walletVersion`). Os contratos tipados estão em `internal/domain/event/`; consumidores devem rotear por `eventType` e `version` e tratar reentregas pelo `eventId`.

O publisher usa `APP_SQS_OUTPUT_QUEUE` (padrão `wager-events.fifo`), `APP_OUTBOX_WORKERS` (`2`), `APP_OUTBOX_POLL_INTERVAL` (`500ms`), `APP_OUTBOX_PROCESSING_TIMEOUT` (`10s`) e `APP_OUTBOX_LEASE` (`30s`). Falhas mantêm o evento na outbox, com `attempts`, `next_attempt_at` e `last_error` consultáveis no PostgreSQL. `last_error` armazena uma categoria segura (`publish_timeout`, `publish_network`, `publish_database` ou `publish_unexpected`), sem a mensagem bruta da dependência; não há descarte após um número fixo de tentativas. Configure o lease acima do timeout de processamento.

O endpoint `/metrics` exige token `internal`. As métricas cobrem resultados financeiros após commit, replays, rejeições, latência, entregas SQS, tamanho aproximado da DLQ, tentativas de referências, backlog e idade da outbox, publicações e republicações, falhas de readiness, divergências de reconciliação e duração do shutdown. Os labels usam categorias limitadas; IDs financeiros e de mensagens ficam apenas em logs estruturados. A fila de saída também participa do readiness. A DLQ monitorada usa `APP_SQS_DLQ_QUEUE` (padrão `wager-transactions-dlq.fifo`).

| Situação | HTTP | Código/estado |
| --- | ---: | --- |
| Operação processada ou rejeitada por regra financeira | 200 | `PROCESSED` ou `REJECTED` com `failureCode` |
| Referência ainda ausente | 202 | `PENDING_REFERENCE` |
| Token ausente, inválido ou sem role | 401/403 | `UNAUTHORIZED` ou `FORBIDDEN` |
| Provider do corpo/caminho diferente do token | 403 | `PROVIDER_MISMATCH` |
| JSON, dinheiro ou chave ausente inválidos | 400 | `INVALID_REQUEST`, `INVALID_MONEY` ou `IDEMPOTENCY_KEY_REQUIRED` |
| Carteira não corresponde a jogador ou moeda | 422 | `WALLET_MISMATCH` |
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

O Compose cria uma role administrativa para migrations e uma role limitada para a aplicação. Em um volume novo, o script `deploy/postgres/init/01-apply-migrations.sh` aplica automaticamente, em ordem lexical, os arquivos `migrations/*.up.sql`:

```sh
docker compose up -d postgres
docker compose ps postgres
```

Os comandos abaixo servem para aplicar versões ausentes em um **volume existente**. Não os execute após `docker compose up` em volume novo: nesse caso as quatro versões já foram aplicadas. Consulte `schema_migrations` e execute somente os arquivos ainda não aplicados, na ordem:

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

Para reverter em ambiente descartável, pare o app (`docker compose stop app`) e execute em ordem inversa; a última etapa remove permanentemente todas as tabelas e seus dados. Reaplique as migrations antes de iniciar o app novamente:

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

O prazo global de parada do Fx é calculado a partir dos limites HTTP, SQS, referência e outbox (95 segundos com os defaults). No Compose, `APP_STOP_GRACE_PERIOD` é 100 segundos por padrão; mantenha-o maior que o orçamento global ao personalizar timeouts. O SQS usa processamento de 20 segundos, visibility de 60 segundos e janela de shutdown de 30 segundos; referências e outbox usam processamento de 10 segundos e lease de 30 segundos.

Deadlocks (`40P01`) e falhas de serialização (`40001`) repetem a transação financeira inteira até três tentativas, com espera curta e jitter. Outros erros de infraestrutura e commits de resultado desconhecido não são repetidos automaticamente; o cliente deve reenviar a mesma identidade quando receber `503 TRANSIENT_FAILURE`.

## Testes

Sem infraestrutura externa, no checkout com Go 1.27:

```sh
go test ./...
go test -race ./...
go vet ./...
```

Para integração, inicie as dependências reais e exporte **no mesmo terminal** as credenciais locais do SQS Gateway e os endpoints usados pelos processos iniciados pelos testes. Os valores abaixo correspondem ao Compose padrão. A suíte usa filas de teste isoladas; pare o app do Compose para evitar disputa pelos workers, e religue-o ao terminar:

```sh
docker compose up -d --build --wait
export APP_DATABASE_URL='postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable'
export APP_DATABASE_ADMIN_URL='postgres://wager_admin:wager_admin_local@localhost:5432/wagering?sslmode=disable'
export APP_OIDC_ISSUER='http://localhost:8081/realms/wagering'
export APP_OIDC_JWKS_URL='http://localhost:8081/realms/wagering/protocol/openid-connect/certs'
export APP_SQS_ENDPOINT='http://localhost:4566'
export APP_SQS_ACCESS_KEY_ID='wager-app-local'
export APP_SQS_SECRET_ACCESS_KEY='wager-app-local-secret'
docker compose stop app
go test -race -tags=integration -p 1 -count=1 \
  ./internal/adapters/postgres ./internal/adapters/auth ./internal/adapters/sqs ./internal/bootstrap
docker compose start app
```

Os exports acima também são pré-requisito para **cada** comando avulso abaixo; se abrir outro terminal, repita o bloco de exports antes de copiá-los. `APP_DATABASE_ADMIN_URL` é usado nos testes de migrations, reconciliação e schema; a suíte de autenticação usa as credenciais administrativas locais do Keycloak fornecidas pelo Compose. Os testes com tag `integration` usam PostgreSQL, Keycloak e LocalStack reais; não são incluídos por `go test ./...`.

```sh
# Três processos com pools próprios, disputa entre carteiras e replay após reinício
go test -race -tags=integration -run '^TestThreeProcessesSerializeAndReplayAfterRestart$' -count=1 -v ./internal/bootstrap
# Queda e recuperação de PostgreSQL/SQS com proxies de falha locais
go test -race -tags=integration -run '^TestTemporaryPostgresAndSQSOutagesRecover$' -count=1 -v ./internal/bootstrap
# SIGTERM durante processamento SQS e retomada em outra instância
go test -race -tags=integration -run '^TestSIGTERMReleasesInFlightSQSMessageForAnotherProcess$' -count=1 -v ./internal/bootstrap
# Referência pendente após reinício completo
go test -race -tags=integration -run '^TestPendingReferencesResolveAndExpireAfterFullProcessRestart$' -count=1 -v ./internal/bootstrap
# Up/down/up de migrations em schema descartável
go test -race -tags=integration -run '^TestMigrationsUpDownUpInDisposableSchema$' -count=1 -v ./internal/adapters/postgres
```

Para testar a partir de um volume novo sem tocar no banco padrão, use outro projeto e portas livres. Depois de `up`, ajuste os mesmos exports do bloco anterior para as quatro portas abaixo, pare `app`, execute a mesma suíte e finalize **somente** o projeto descartável com `down -v`:

```sh
export KEYCLOAK_PORT=18081 POSTGRES_PORT=15432 LOCALSTACK_PORT=14566 APP_HTTP_PORT=18080
docker compose -p wager-clean-smoke up -d --build --wait
curl -f http://localhost:18080/health/ready
docker compose -p wager-clean-smoke exec -T postgres \
  psql -U wager_admin -d wagering -Atc "SELECT string_agg(version::text, ',' ORDER BY version) FROM schema_migrations"
export APP_DATABASE_URL='postgres://wager_app:wager_app_local@localhost:15432/wagering?sslmode=disable'
export APP_DATABASE_ADMIN_URL='postgres://wager_admin:wager_admin_local@localhost:15432/wagering?sslmode=disable'
export APP_OIDC_ISSUER='http://localhost:18081/realms/wagering'
export APP_OIDC_JWKS_URL='http://localhost:18081/realms/wagering/protocol/openid-connect/certs'
export APP_SQS_ENDPOINT='http://localhost:14566'
export APP_SQS_ACCESS_KEY_ID='wager-app-local'
export APP_SQS_SECRET_ACCESS_KEY='wager-app-local-secret'
docker compose -p wager-clean-smoke stop app
go test -race -tags=integration -p 1 -count=1 \
  ./internal/adapters/postgres ./internal/adapters/auth ./internal/adapters/sqs ./internal/bootstrap
docker compose -p wager-clean-smoke down -v
```

A suíte multiprocesso compila o binário e inicia três instâncias em portas livres. Os testes de falha e shutdown usam proxies ou filas isoladas; o de migrations cria e remove um schema próprio. Os testes de referência retomam pendências após reinício. Troque as portas do ensaio descartável se já estiverem ocupadas.
