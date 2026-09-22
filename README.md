# Processamento Distribuído de Apostas em Go

Implementação em andamento do desafio descrito em `teste tecnoco.md`. A arquitetura e a sequência de trabalho estão em `ARCHITECTURE.md` e `IMPLEMENTATION_PLAN.md`.

## Estado atual

O projeto já inclui o bootstrap com Uber Fx, configuração validada, logs JSON, servidor HTTP, graceful shutdown e o domínio financeiro (`Money`, carteira, transações, ledger e eventos). PostgreSQL, Keycloak, SQS e os casos de uso transacionais serão adicionados nas próximas fases.

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

## PostgreSQL e migrations

O Compose cria uma role administrativa para migrations e uma role limitada para a aplicação. Em um volume novo, a migration inicial é aplicada automaticamente:

```sh
docker compose up -d postgres
docker compose ps postgres
```

Aplicação manual em um banco vazio:

```sh
docker compose exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U wager_admin -d wagering \
  -f /migrations/000001_initial.up.sql
```

Reversão, que remove permanentemente todas as tabelas e seus dados:

```sh
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
```
