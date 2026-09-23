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

O caso de uso delimita a transação por meio de `UnitOfWork`; os repositórios não abrem nem confirmam transações. Eles recebem a mesma interface `DBTX`, satisfeita por `pgx.Tx`, para que carteira, transação de aposta e ledger participem do mesmo commit. Consultas de reconciliação usam uma unidade de trabalho separada em `REPEATABLE READ READ ONLY`.

Além do lock pessimista, o `UPDATE` da carteira compara a versão anterior e exige exatamente uma linha afetada. Isso detecta uso incorreto de um agregado obsoleto sem substituir a serialização por carteira.

### Mapeamento PostgreSQL

Os repositórios usam SQL explícito e reconstroem agregados pelos construtores de reidratação do domínio. `Money` é persistido como `BIGINT` em unidades mínimas mais `CHAR(3)` para moeda; não há conversão intermediária por ponto flutuante. UUIDs são lidos como texto, campos opcionais usam tipos anuláveis do `pgx` e o hash canônico é armazenado como `BYTEA` de 32 bytes.

`WalletRepository` concentra leitura simples, leitura com `FOR NO KEY UPDATE`, inserção e atualização versionada. `WagerRepository` oferece as três identidades necessárias para replay e conflito: ID interno, `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`. `LedgerRepository` oferece somente inserção, coerente com o ledger append-only; o banco também bloqueia mutações diretas.

### Idempotência

O banco imporá unicidade de `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`. Um SHA-256 do payload de negócio canônico detectará reuso da identidade com conteúdo diferente. Replays reproduzirão o resultado persistido, inclusive o saldo observado no processamento original.

O payload canônico é JSON UTF-8 compacto com chaves em ordem lexicográfica. Ele contém `externalTransactionId`, `gameId`, `kind`, `money` (`amount`, `currency`), `playerId`, `providerId`, `referenceExternalTransactionId` quando presente, `roundId` e `walletId`. O valor monetário é normalizado para duas casas decimais e a moeda para o código de três letras aceito pelo domínio. `Idempotency-Key`, correlação, credenciais e metadados de HTTP/SQS ficam fora do hash. O mesmo construtor será usado pelo adaptador SQS, preservando equivalência entre transportes.

Em `READ COMMITTED`, uma inserção concorrente pode tornar-se visível entre as consultas das duas identidades. Quando isso ocorre, o caso de uso reavalia chave e hash usando a linha vencedora; uma entrega idêntica vira replay, enquanto conteúdo ou chave divergentes continuam sendo conflito. Violações concorrentes dos índices únicos são traduzidas para a mesma avaliação após rollback.

### Ledger

O ledger será append-only. Reversões criam novos lançamentos e não editam histórico. Constraints, FKs, triggers diferíveis e privilégios da role de runtime protegerão a correspondência entre saldo, transação e lançamento, além de impedir `UPDATE`, `DELETE` e `TRUNCATE`.

### Referências e reversões

Uma `BET` admite uma única reversão direta bem-sucedida, `REFUND` ou `ROLLBACK`; `WIN` e `REFUND` admitem um `ROLLBACK`. Desfazer um `REFUND` não reabre a aposta para outra reversão. Referências ainda ausentes são persistidas com agenda, expiração e lease; outra instância pode retomá-las.

### Inbox e outbox

A inbox deduplica mensagens por consumidor e `messageId`, verificando também o hash do envelope. A conclusão da inbox compartilha o commit dos efeitos de negócio. Eventos são gravados na outbox antes da publicação; publishers concorrentes usam lease e token. Uma queda após o envio pode republicar o mesmo `eventId`, portanto a entrega externa permanece at-least-once.

O consumidor usa AWS SDK for Go v2 e long polling. O envelope é validado e normalizado antes da transação; seu hash SHA-256 inclui `data`, `messageId`, `occurredAt` e `type`, mas não atributos de entrega do broker. A sessão de wagering pode ser executada dentro da transação aberta pela ingestão, permitindo confirmar inbox, carteira, transação, ledger e outbox atomicamente. A mensagem SQS só é apagada depois desse commit; falha no delete provoca reentrega segura.

### Autenticação e autorização

O HTTP usará OAuth 2.0/OIDC com Keycloak local e `client_credentials`. O token determina o provider autorizado; valores do corpo nunca concedem autoridade. Endpoints de carteira e reconciliação exigem identidade interna. A fila de entrada aceita apenas um produtor interno confiável, porque `providerId` no payload não autentica o remetente.

O adaptador usa `go-oidc` 3.21.0 para verificar assinatura RS256, emissor, audiência e validade temporal. Em Docker, o emissor público (`localhost:8081`) permanece o valor validado no token, enquanto uma URL JWKS interna (`keycloak:8080`) é configurada separadamente; isso evita desabilitar a validação de issuer apenas para contornar DNS entre host e containers. O claim `provider_id` identifica o provedor e `realm_access.roles` determina as permissões `provider` e `internal`.

O realm de teste contém dois clients de provedor para demonstrar isolamento com tokens reais. A fila SQS compartilhada é uma fronteira de confiança diferente do HTTP: apenas o serviço interno de ingestão deve enviar, usando uma role IAM separada da role do aplicativo. Os templates de menor privilégio estão em `deploy/aws/`, mas o LocalStack Community local aceitou credenciais fictícias para consultar a fila. Portanto, o ambiente local não comprova negação por IAM; a evidência pendente e as demais lacunas estão em `REQUIREMENTS_AUDIT.md`.

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
- `pgxpool` fixado na versão 5.10.0, validado no startup e fechado pelo lifecycle do Fx;
- readiness dinâmico que consulta PostgreSQL com timeout, sem afetar liveness;
- unidade de trabalho com `READ COMMITTED` para operações e `REPEATABLE READ READ ONLY` para reconciliação.
- repositórios PostgreSQL explícitos para carteiras, transações e ledger, fornecidos pelo módulo Fx;
- lock de carteira com `FOR NO KEY UPDATE`, atualização com guarda de versão e erros classificáveis de ausência/conflito;
- mapeamento completo dos agregados financeiros, incluindo nulos e hash binário;
- testes de integração dos repositórios, commit diferido, rollback e constraints em PostgreSQL real.
- migration incremental que relaciona tipo da aposta, direção do ledger, referência e resultado financeiro histórico;
- agenda obrigatória para persistência de `PENDING_REFERENCE`;
- Keycloak 26.7.3 provisionado com clients de serviço, audiência e roles;
- verificação JWT via JWKS, middleware de autenticação e autorização por role;
- caso de uso de abertura de carteira e persistência atômica de `OPENING`, ledger e outbox;
- endpoints internos de abertura, leitura e ledger com paginação por cursor opaco.
- caso de uso transacional compartilhável para `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`;
- idempotência persistente pelas duas identidades, SHA-256 canônico e replay do resultado financeiro histórico;
- serialização por lock da carteira, ledger e outbox no mesmo commit, inclusive para rejeições auditáveis;
- referências válidas, reversões únicas e persistência durável de referências ainda ausentes;
- endpoints autenticados de envio e consulta de transações, sempre isolados pelo provider do token;
- testes reais de concorrência para 50 duplicatas e duas apostas de `80.00` sobre saldo de `100.00`.
- LocalStack 4.14.0 com fila de entrada FIFO, DLQ, redrive e fila FIFO de eventos;
- consumidor SQS com long polling, concorrência limitada, shutdown coordenado e readiness;
- backpressure no polling: o batch solicitado nunca excede os slots de workers disponíveis, evitando mensagens invisíveis aguardando capacidade local;
- envelope estrito com hash canônico e inbox PostgreSQL transacional compartilhando o commit financeiro;
- reentrega e cruzamento HTTP/SQS com um único efeito financeiro, validados no PostgreSQL e LocalStack reais;
- registry Prometheus privado, endpoint `/metrics` e métricas SQS de recebimento, reentrega, resultado e duração sem IDs como labels;
- recuperação automatizada da falha entre commit e delete: a mensagem não confirmada volta como replay e pode então ser removida com segurança;
- redrive real automatizado e três consumidores independentes validados com filas isoladas, clientes SQS e pools PostgreSQL próprios.
- worker de referências pendentes com reivindicação curta via `FOR UPDATE SKIP LOCKED`, lease persistido e token obrigatório nas atualizações;
- continuação financeira sob lock da carteira, com reavaliação da referência, TTL de 24 horas, backoff com jitter e eventos terminais no mesmo commit;
- testes em PostgreSQL real para resolução tardia, referência rejeitada/incompatível, expiração, reaquisição de lease por outro pool e rejeição de token obsoleto.
- publisher concorrente da outbox com reivindicações curtas, lease/token no PostgreSQL, retry limitado com jitter e confirmação somente após aceitação pelo SQS;
- publicação FIFO por carteira e `eventId` estável para deduplicação no consumidor, com recuperação após falha entre envio e confirmação;
- testes de concorrência e recuperação em PostgreSQL real, além do fluxo completo até a fila de eventos do LocalStack.
- reconciliação autenticada com transação `REPEATABLE READ READ ONLY`, soma `NUMERIC` no PostgreSQL e checagem explícita de overflow antes de converter para `Money`;
- métricas de resultados confirmados, replays, filas, retries, divergência e dependências com labels controlados, além de logs de correlação sem payloads ou credenciais;
- `/metrics` restrito ao papel interno; readiness inclui PostgreSQL, Keycloak, filas de entrada, saída e DLQ.

O teste multiprocesso da Fase 10 inicia três cópias reais do binário com portas e pools próprios e usa barreiras para a disputa financeira. Uma conexão PostgreSQL segura o lock da carteira A; o teste observa outra conexão aguardando `FOR NO KEY UPDATE` antes de comprovar que a carteira B avança por outra instância. Também encerra os três processos por SIGTERM, reinicia-os e demonstra replay persistente, conferindo saldo, versão, ledger e outbox por SQL.

Os testes adicionais seguram o lock da carteira até duas reversões concorrentes aguardarem no PostgreSQL; após a liberação, apenas uma de `REFUND` ou `ROLLBACK` confirma, e a rejeição preserva o saldo histórico mesmo após nova operação. Outro cenário encerra completamente o processo com duas referências pendentes e inicia um substituto: a primeira é resolvida quando sua aposta chega, a segunda expira com evento de rejeição. Ambos os resultados são conferidos por API, saldo, ledger e outbox.

Um teste de falha isolado usa proxies locais que podem cortar conexões PostgreSQL existentes e responder indisponibilidade no SQS, sem alterar os containers compartilhados. Erros de conexão fechada e de transporte do `pgx`, além dos SQLSTATEs transitórios, tornam-se `503 TRANSIENT_FAILURE`; o cliente deve repetir a identidade original, inclusive quando não souber se houve commit. A perda do SQS não desfaz o commit financeiro: a outbox permanece durável e o publisher retoma o envio após recuperação. A classificação de conexão fechada segue a semântica de `pgconn.ErrConnClosed` documentada no pgx 5.10.0.

A migration 3 garante que uma referência pendente nunca fique agendada depois de `expires_at`; antes de criar a constraint, normaliza linhas antigas para vencerem exatamente no prazo. Assim, o claim usa apenas `next_attempt_at <= statement_timestamp()` e pode limitar a varredura pelo índice parcial existente. A mesma troca de relógio no claim da outbox fez o predicado de prazo aparecer como `Index Cond` no `EXPLAIN` local. Ledger e inbox usam os índices já existentes; a estatística de backlog pode exigir varrer todos os eventos pendentes para calcular a contagem. Os planos locais foram obtidos com poucos dados e não são alegação de performance em escala. O arquivo `scripts/explain_worker_queries.sql` reproduz essa inspeção sem mutação.

No shutdown, os hooks Fx rodam em ordem inversa e compartilham um prazo global. O timeout agora é calculado a partir dos limites de HTTP, SQS, referência e outbox mais uma margem (95s nos defaults); o Compose concede 100s antes de forçar a parada. Ao alterar esses limites, aumentar também `APP_STOP_GRACE_PERIOD`. A revisão final de entrega ainda está pendente.
