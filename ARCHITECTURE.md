# Arquitetura

## Status

Documento inicial. As decisões abaixo orientam a implementação e serão atualizadas com as evidências dos testes. O enunciado em `teste tecnoco.md` é a fonte de requisitos e `IMPLEMENTATION_PLAN.md` mantém a rastreabilidade.

## Limite do sistema

O serviço recebe operações por HTTP autenticado e por uma fila produzida por um serviço interno confiável. Ambos os adaptadores usam o mesmo caso de uso e a mesma transação PostgreSQL. O PostgreSQL é a fonte da verdade; FIFO, memória local e locks do processo não garantem correção financeira.

## Decisões aceitas

### Dinheiro

`Money` usa `int64` em unidades mínimas, moeda explícita e escala fixa de duas casas. Entradas e saídas usam strings decimais. Nenhum caminho faz conversão por ponto flutuante. Parsing, soma, subtração e negação verificam overflow e compatibilidade de moeda.

O parser interno aceita sinal para permitir diferenças e cálculos negativos. O parser de entrada externa recusa qualquer sinal negativo, inclusive `-0.00`, e exige exatamente duas casas. Representações com zeros à esquerda são aceitas e normalizadas antes de serialização e do futuro hash canônico (`00025.00` torna-se `25.00`). A lista inicial de moedas suportadas é BRL, USD e EUR; cenários financeiros principais permanecem em BRL.

### Concorrência e transações

Operações financeiras usarão transação `READ COMMITTED` e lock pessimista por carteira com `SELECT ... FOR NO KEY UPDATE`. Isso serializa apenas operações da mesma carteira. Saldo, versão, transação, ledger, inbox quando aplicável e outbox serão confirmados atomicamente.

Todos os fluxos seguirão a ordem de locks definida na seção 11.2 do plano. Deadlocks e falhas de serialização provocam retry limitado da transação inteira; não viram rejeição de negócio.

### Idempotência

O banco imporá unicidade de `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`. Um SHA-256 do payload de negócio canônico detectará reuso da identidade com conteúdo diferente. Replays reproduzirão o resultado persistido, inclusive o saldo observado no processamento original.

### Ledger

O ledger será append-only. Reversões criam novos lançamentos e não editam histórico. Constraints, FKs, triggers diferíveis e privilégios da role de runtime protegerão a correspondência entre saldo, transação e lançamento, além de impedir `UPDATE`, `DELETE` e `TRUNCATE`.

### Referências e reversões

Uma `BET` admite uma única reversão direta bem-sucedida, `REFUND` ou `ROLLBACK`; `WIN` e `REFUND` admitem um `ROLLBACK`. Desfazer um `REFUND` não reabre a aposta para outra reversão. Referências ainda ausentes são persistidas com agenda, expiração e lease; outra instância pode retomá-las.

### Inbox e outbox

A inbox deduplica mensagens por consumidor e `messageId`, verificando também o hash do envelope. A conclusão da inbox compartilha o commit dos efeitos de negócio. Eventos são gravados na outbox antes da publicação; publishers concorrentes usam lease e token. Uma queda após o envio pode republicar o mesmo `eventId`, portanto a entrega externa permanece at-least-once.

### Autenticação e autorização

O HTTP usará OAuth 2.0/OIDC com Keycloak local e `client_credentials`. O token determina o provider autorizado; valores do corpo nunca concedem autoridade. Endpoints de carteira e reconciliação exigem identidade interna. A fila de entrada aceita apenas um produtor interno confiável, porque `providerId` no payload não autentica o remetente.

### Composição e encerramento

Uber Fx compõe configuração, logger, recursos, adaptadores e workers em módulos. Recursos registram `OnStart`/`OnStop` no lifecycle. No encerramento, readiness cai primeiro, novas entradas param e o trabalho em andamento recebe prazo antes do fechamento das dependências.

## Implementado nesta etapa

- módulo Go e binário composto por Fx;
- configuração por ambiente com validação;
- logger JSON com `slog`;
- servidor `net/http` com timeouts, liveness, readiness e graceful shutdown;
- build multi-stage e execução local por Docker Compose;
- testes unitários do bootstrap HTTP e configuração.
- value object `Money` com parsing estrito, serialização decimal, moedas e proteção de overflow;
- agregado `Wallet` com saldo não negativo, débito/crédito e versionamento;
- `WagerTransaction` com origens, seis tipos, cinco estados e transições terminais protegidas;
- ledger com equação de crédito/débito validada e versão da carteira;
- envelopes e payloads tipados para os quatro eventos obrigatórios;
- criação e reidratação separadas, sem emissão de eventos durante reidratação.
- PostgreSQL 18 no Compose, com volume persistente, healthcheck e migrations versionadas;
- roles distintas para migrations e runtime, sem `DELETE`/`TRUNCATE` no ledger;
- schema inicial para carteiras, transações, ledger, inbox e outbox;
- constraints diferíveis que exigem correspondência entre saldo, versão, transação processada e ledger;
- triggers que protegem ledger, identidade/estado terminal das transações e snapshot da outbox.

O pool `pgx`, repositórios, Keycloak, SQS e casos de uso transacionais ainda serão acrescentados nas próximas fases. Os health checks atuais representam apenas o processo HTTP; readiness passará a agregar PostgreSQL e SQS quando essas dependências forem conectadas à aplicação.
