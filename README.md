# Processamento Distribuído de Apostas em Go

Implementação em andamento do desafio descrito em `teste tecnoco.md`. A arquitetura e a sequência de trabalho estão em `ARCHITECTURE.md` e `IMPLEMENTATION_PLAN.md`.

## Estado atual

O bootstrap da aplicação já inclui Go Modules, composição com Uber Fx, configuração validada, logs JSON, servidor HTTP e graceful shutdown. PostgreSQL, Keycloak, SQS e os fluxos financeiros serão adicionados nas próximas fases.

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

## Verificações

```sh
go test ./...
go test -race ./...
go vet ./...
```
