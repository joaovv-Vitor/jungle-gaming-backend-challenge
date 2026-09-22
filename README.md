# Processamento Distribuído de Apostas em Go

Implementação em andamento do desafio descrito em `teste tecnoco.md`. A arquitetura e a sequência de trabalho estão em `ARCHITECTURE.md` e `IMPLEMENTATION_PLAN.md`.

## Estado atual

O projeto inclui bootstrap com Uber Fx, domínio financeiro, PostgreSQL, Keycloak/OIDC e processamento idempotente por HTTP e SQS de `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`. No SQS, inbox, carteira, transação, ledger e outbox são confirmados atomicamente antes da remoção da mensagem. Workers duráveis retomam referências pendentes e publicam a outbox na fila de eventos FIFO.

## Requisitos locais

- Go 1.27;
- Docker com Docker Compose.

## Executar localmente

```sh
cp .env.example .env
go run ./cmd/server
```

Verifique o processo:

```sh
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
```

Com Docker:

```sh
docker compose up --build
```

O Compose provisiona o realm `wagering`, os clients `internal-service`, `provider-a` e `wager-api`, além das roles `internal` e `provider`. As credenciais abaixo são exclusivamente locais.

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

O cursor devolvido pelo ledger é opaco e deve ser reenviado sem alterações no parâmetro `cursor`.

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

### Operações via SQS

O Compose provisiona `wager-transactions.fifo`, `wager-transactions-dlq.fifo` e `wager-events.fifo` no LocalStack. A fila de entrada usa long polling de 20 segundos, visibility de 60 segundos e redrive após cinco recebimentos. Envie mensagens usando `walletId` como `MessageGroupId` e uma identidade de transporte estável como `MessageDeduplicationId`:

```sh
docker compose exec -T localstack awslocal sqs send-message \
  --queue-url http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/wager-transactions.fifo \
  --message-group-id WALLET_ID \
  --message-deduplication-id msg-123 \
  --message-body '{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"transaction-123","idempotencyKey":"provider-a:transaction-123","playerId":"PLAYER_ID","walletId":"WALLET_ID","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}'
```

O `messageId` do envelope identifica a inbox. Reentregas com o mesmo conteúdo são confirmadas sem reaplicar o efeito; o mesmo `messageId` com conteúdo diferente permanece na fila para redrive. Um `messageId` novo ainda é deduplicado pelas identidades financeiras compartilhadas com o HTTP.

Eventos financeiros são gravados na outbox no mesmo commit da operação e publicados depois em `wager-events.fifo`. O envio é *at-least-once*: se houver queda após o envio e antes da confirmação no banco, o mesmo `eventId` pode ser publicado novamente. Consumidores da fila de eventos devem deduplicar por `eventId`; a deduplicação temporária da FIFO não substitui essa regra. `MessageGroupId` usa a carteira, e `MessageDeduplicationId` usa o `eventId`. Publicações de workers distintos podem chegar fora da ordem dos commits; `walletVersion` permite identificar lacunas nos eventos de saldo.

O publisher usa `APP_SQS_OUTPUT_QUEUE` (padrão `wager-events.fifo`), `APP_OUTBOX_WORKERS` (`2`), `APP_OUTBOX_POLL_INTERVAL` (`500ms`), `APP_OUTBOX_PROCESSING_TIMEOUT` (`10s`) e `APP_OUTBOX_LEASE` (`30s`). Falhas mantêm o evento na outbox, com `attempts`, `next_attempt_at` e `last_error` consultáveis no PostgreSQL; não há descarte após um número fixo de tentativas. Configure o lease acima do timeout de processamento.

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
| Falha concorrente transitória | 503 | `TRANSIENT_FAILURE` |

### Respostas dos endpoints de carteira

| Situação | HTTP | Código |
| --- | ---: | --- |
| Token ausente, inválido ou expirado | 401 | `UNAUTHORIZED` |
| Identidade sem a role interna | 403 | `FORBIDDEN` |
| JSON, dinheiro, UUID ou paginação inválidos | 400 | `INVALID_REQUEST`, `INVALID_MONEY`, `INVALID_WALLET_ID` ou `INVALID_PAGINATION` |
| Carteira inexistente | 404 | `WALLET_NOT_FOUND` |
| Jogador e moeda já possuem carteira | 409 | `WALLET_ALREADY_EXISTS` |
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
```

Reversão em ordem inversa; a última etapa remove permanentemente todas as tabelas e seus dados:

```sh
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000002_financial_semantics.down.sql
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000001_initial.down.sql
```

As credenciais de `.env.example` são apenas para o ambiente local.

## Verificações

```sh
go test ./...
go test -race ./...
go vet ./...
go test -tags=integration ./internal/adapters/postgres
go test -tags=integration ./internal/adapters/auth
go test -tags=integration ./internal/adapters/sqs
go test -tags=integration -run TestThreeProcessesSerializeAndReplayAfterRestart -v ./internal/bootstrap
```

Os testes com tag `integration` exigem os serviços correspondentes ativos pelo Compose. Para evitar disputa pelas fixtures de referência e outbox, pare apenas o app durante as suítes PostgreSQL/SQS (`docker compose stop app`) e religue-o depois (`docker compose start app`). O teste `internal/bootstrap` constrói o binário e inicia três processos adicionais em portas livres; pode rodar com o app do Compose ativo e registra seus PIDs com `-v`. Ele exercita concorrência, lock entre carteiras e replay depois de reiniciar todas as três instâncias, sem parar PostgreSQL, Keycloak ou LocalStack. A suíte SQS cria filas FIFO isoladas, valida redrive e três consumidores concorrentes contra LocalStack e PostgreSQL reais e remove as filas ao final. O cenário de divergência da reconciliação usa a role administrativa local para alterar somente a carteira criada pelo teste e restaura seu saldo; em outra configuração, informe `APP_DATABASE_ADMIN_URL`.
