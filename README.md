# Processamento Distribuído de Apostas em Go

Implementação em andamento do desafio descrito em `teste tecnoco.md`. A arquitetura e a sequência de trabalho estão em `ARCHITECTURE.md` e `IMPLEMENTATION_PLAN.md`.

## Estado atual

O projeto inclui bootstrap com Uber Fx, domínio financeiro, PostgreSQL, Keycloak/OIDC e o fluxo autenticado de carteiras. Uma abertura positiva confirma carteira, `OPENING`, ledger e dois eventos de outbox no mesmo commit. SQS e o processamento das operações externas permanecem em implementação.

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
```

O cursor devolvido pelo ledger é opaco e deve ser reenviado sem alterações no parâmetro `cursor`.

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
```

Os testes com tag `integration` exigem PostgreSQL e Keycloak ativos pelo Compose.
