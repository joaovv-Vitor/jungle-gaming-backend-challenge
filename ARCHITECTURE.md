# Arquitetura

## Limite do sistema

O serviço recebe operações por HTTP autenticado e por uma fila produzida por um serviço interno confiável. Ambos os adaptadores usam o mesmo caso de uso e a mesma transação PostgreSQL. O PostgreSQL é a fonte da verdade; FIFO, memória local e locks do processo não garantem correção financeira.

## Decisões aceitas

### Dinheiro

`Money` usa `int64` em unidades mínimas, moeda explícita e escala fixa de duas casas. Entradas e saídas usam strings decimais. Nenhum caminho faz conversão por ponto flutuante. Parsing, soma, subtração e negação verificam overflow e compatibilidade de moeda.

Com duas casas, o máximo positivo é `92233720368547758.07` por moeda; o mínimo negativo de `int64` só serve a diferenças internas. Entradas financeiras externas não aceitam valores negativos. Essa representação evita arredondamento binário e persiste sem perda como `BIGINT` e moeda `CHAR(3)` no PostgreSQL.

O parser interno aceita sinal para permitir diferenças e cálculos negativos. O parser de entrada externa recusa qualquer sinal negativo, inclusive `-0.00`, e exige exatamente duas casas. Representações com zeros à esquerda são aceitas e normalizadas antes de serialização e do hash canônico (`00025.00` torna-se `25.00`). A lista inicial de moedas suportadas é BRL, USD e EUR; cenários financeiros principais permanecem em BRL.

### Concorrência e transações

Operações financeiras usam transação `READ COMMITTED` e lock pessimista por carteira com `SELECT ... FOR NO KEY UPDATE`. Isso serializa apenas operações da mesma carteira. Saldo, versão, transação, ledger, inbox quando aplicável e outbox são confirmados atomicamente.

Os fluxos financeiros bloqueiam primeiro a carteira e depois consultam ou atualizam a transação e seus registros dependentes. Deadlocks e falhas de serialização provocam retry limitado da transação inteira; não viram rejeição de negócio.

O retry transacional é aplicado apenas a `40P01` e `40001`, até três tentativas com jitter. Cada tentativa abre uma transação nova e repete o callback completo; erros de conexão e commits ambíguos não são repetidos automaticamente. I/O externo permanece fora do callback.

O caso de uso delimita a transação por meio de `UnitOfWork`; os repositórios não abrem nem confirmam transações. Eles recebem a mesma interface `DBTX`, satisfeita por `pgx.Tx`, para que carteira, transação de aposta e ledger participem do mesmo commit. Consultas de reconciliação usam uma unidade de trabalho separada em `REPEATABLE READ READ ONLY`.

Além do lock pessimista, o `UPDATE` da carteira compara a versão anterior e exige exatamente uma linha afetada. Isso detecta uso incorreto de um agregado obsoleto sem substituir a serialização por carteira.

### Mapeamento PostgreSQL

Os repositórios usam SQL explícito e reconstroem agregados pelos construtores de reidratação do domínio. `Money` é persistido como `BIGINT` em unidades mínimas mais `CHAR(3)` para moeda; não há conversão intermediária por ponto flutuante. UUIDs são lidos como texto, campos opcionais usam tipos anuláveis do `pgx` e o hash canônico é armazenado como `BYTEA` de 32 bytes.

`WalletRepository` concentra leitura simples, leitura com `FOR NO KEY UPDATE`, inserção e atualização versionada. `WagerRepository` oferece as três identidades necessárias para replay e conflito: ID interno, `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`. `LedgerRepository` oferece somente inserção, coerente com o ledger append-only; o banco também bloqueia mutações diretas.

A leitura paginada do ledger usa `(wallet_version, id)` em ordem descendente e cursor opaco com versão de formato, carteira e última chave. A próxima página exige chave estritamente menor; como as novas movimentações recebem versões maiores sob lock e o ledger é append-only, escritas posteriores à primeira página não repetem nem deslocam os lançamentos mais antigos. O limite HTTP é 1–100, com padrão 50.

### Idempotência

O banco impõe unicidade de `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`. Um SHA-256 do payload de negócio canônico detecta reuso da identidade com conteúdo diferente. Replays reproduzem o resultado persistido, inclusive o saldo observado no processamento original.

O payload canônico é JSON UTF-8 compacto com chaves em ordem lexicográfica. Ele contém `externalTransactionId`, `gameId`, `kind`, `money` (`amount`, `currency`), `playerId`, `providerId`, `referenceExternalTransactionId` quando presente, `roundId` e `walletId`. O valor monetário é normalizado para duas casas decimais e a moeda para o código de três letras aceito pelo domínio. `Idempotency-Key`, correlação, credenciais e metadados de HTTP/SQS ficam fora do hash. O mesmo construtor é usado pelo adaptador SQS, preservando equivalência entre transportes.

Em `READ COMMITTED`, uma inserção concorrente pode tornar-se visível entre as consultas das duas identidades. Quando isso ocorre, o caso de uso reavalia chave e hash usando a linha vencedora; uma entrega idêntica vira replay, enquanto conteúdo ou chave divergentes continuam sendo conflito. Violações concorrentes dos índices únicos são traduzidas para a mesma avaliação após rollback.

### Ledger

O ledger é append-only. Reversões criam novos lançamentos e não editam histórico. Constraints, FKs, triggers diferíveis e privilégios da role de runtime protegem a correspondência entre saldo, transação e lançamento, além de impedir `UPDATE`, `DELETE` e `TRUNCATE`.

A migration 4 verifica o histórico antes da instalação e faz a trigger diferível exigir que `balance_before_minor` seja exatamente o saldo anterior da carteira (zero na abertura), além de conferir saldo posterior, versão, transação, valor e direção. Isso impede que um lançamento comece de um saldo inventado mesmo quando a equação interna do lançamento está correta.

### Referências e reversões

Uma `BET` admite uma única reversão direta bem-sucedida, `REFUND` ou `ROLLBACK`; `WIN` e `REFUND` admitem um `ROLLBACK`. Desfazer um `REFUND` não reabre a aposta para outra reversão. Referências ainda ausentes são persistidas com agenda, expiração e lease; outra instância pode retomá-las.

`REFUND` credita integralmente uma `BET`; `ROLLBACK` aplica o movimento inverso à `BET`, `WIN` ou `REFUND` referenciada. A referência é resolvida por provedor e ID externo, com verificação de jogador, carteira, rodada, moeda e valor. Uma constraint impede duas reversões diretas bem-sucedidas da mesma operação. Se o movimento inverso precisaria debitar mais que o saldo, a transação é `REJECTED` com `REVERSAL_INSUFFICIENT_FUNDS`, distinto de `BET_INSUFFICIENT_FUNDS`.

Uma referência ausente ou ainda pendente mantém a transação em `PENDING_REFERENCE`. Workers a reivindicam com `FOR UPDATE SKIP LOCKED` e lease/token persistidos; reavaliam sob lock da carteira e reagendam com backoff exponencial de 1 segundo a 5 minutos e jitter, sem ultrapassar o TTL de 24 horas. A agenda sobrevive ao reinício. Referência rejeitada ou incompatível causa rejeição definitiva; se ausente ou pendente ao fim do TTL, o resultado é `REJECTED` com `REFERENCE_NOT_FOUND` e evento na outbox.

### Estados e falhas permanentes

O domínio valida `PENDING → PROCESSED`, `PENDING → PENDING_REFERENCE`, `PENDING → REJECTED` e `PENDING → FAILED`, além das transições de `PENDING_REFERENCE` para estados terminais. `PROCESSED`, `REJECTED` e `FAILED` são terminais. `REJECTED` representa uma decisão de negócio persistida com `failureCode` e saldo resultante; `FAILED` representa uma falha de infraestrutura comprovadamente permanente, com `INFRASTRUCTURE_PERMANENT_FAILURE` e sem saldo resultante. `Transaction.Fail()` valida essa transição e a reidratação aceita o estado, mas nenhum worker atual a aciona ou persiste. O enunciado exige validar a transição no domínio e explicar a política, sem exigir uma falha permanente simulada no fluxo de produção.

Uma falha de conexão, timeout, deadlock, indisponibilidade do SQS ou resultado de commit incerto não comprova permanência. O callback SQL é repetido apenas para deadlock e erro de serialização; outros erros transitórios retornam `503 TRANSIENT_FAILURE` no HTTP para repetição com a mesma identidade. No SQS, a mensagem permanece sem delete e a visibility recebe backoff antes de nova entrega. Após o limite de recebimentos, o broker move envelopes inválidos e falhas não resolvidas para a DLQ para inspeção e eventual reenvio controlado. Esse redrive não converte automaticamente uma transação financeira em `FAILED`: a transação pode nem ter sido criada, e um commit incerto exige consulta antes de qualquer decisão terminal. Referência ausente ou ainda pendente segue sua agenda durável; ao fim do TTL é `REJECTED` com `REFERENCE_NOT_FOUND`, não `FAILED`. Uma classificação permanente só seria apropriada após evidência operacional conclusiva de que não haverá recuperação ou efeito financeiro posterior, com registro atômico e auditável dessa decisão.

### Inbox e outbox

A inbox deduplica mensagens por consumidor e `messageId`, verificando também o hash do envelope. A conclusão da inbox compartilha o commit dos efeitos de negócio. Eventos são gravados na outbox antes da publicação; publishers concorrentes usam lease e token. Uma queda após o envio pode republicar o mesmo `eventId`, portanto a entrega externa permanece at-least-once.

Publishers reivindicam um evento por vez em transações curtas com `FOR UPDATE SKIP LOCKED`; o token impede que um worker antigo confirme o envio após perder o lease. Falhas deixam o evento elegível para retry com backoff exponencial de 1 segundo a 5 minutos e jitter, sem limite fixo de tentativas. Após crash, o lease expira e outra instância retoma. Só a confirmação do envio ao SQS marca `published_at`; consumidores deduplicam pelo `eventId` estável.

No consumidor SQS, erros transitórios de processamento alteram a visibility com backoff exponencial de 5 segundos, dobrando por recebimento até 300 segundos. Envelopes inválidos e conflitos permanentes permanecem para o redrive; uma falha ao alterar a visibility mantém a reentrega original segura.

O consumidor usa AWS SDK for Go v2 e long polling. O envelope é validado e normalizado antes da transação; seu hash SHA-256 inclui `data`, `messageId`, `occurredAt` e `type`, mas não atributos de entrega do broker. A sessão de wagering roda dentro da transação aberta pela ingestão, confirmando inbox, carteira, transação, ledger e outbox atomicamente. A mensagem SQS só é apagada depois desse commit; falha no delete provoca reentrega segura. FIFO agrupa por carteira (`MessageGroupId`); `MessageDeduplicationId` protege duplicação de transporte por uma janela curta, sem substituir a inbox.

### Autenticação e autorização

O HTTP usa OAuth 2.0/OIDC com Keycloak local e `client_credentials`. O token determina o provider autorizado; valores do corpo nunca concedem autoridade. Endpoints de carteira e reconciliação exigem identidade interna. A fila de entrada aceita apenas um produtor interno confiável, porque `providerId` no payload não autentica o remetente.

O adaptador usa `go-oidc` 3.21.0 para verificar assinatura RS256, emissor, audiência e validade temporal. Em Docker, o emissor público (`localhost:8081`) permanece o valor validado no token, enquanto uma URL JWKS interna (`keycloak:8080`) é configurada separadamente; isso evita desabilitar a validação de issuer apenas para contornar DNS entre host e containers. O claim `provider_id` identifica o provedor e `realm_access.roles` determina as permissões `provider` e `internal`.

O realm de teste contém dois clients de provedor para demonstrar isolamento com tokens reais. A fila SQS compartilhada é uma fronteira de confiança diferente do HTTP: apenas o serviço interno de ingestão pode enviar, com credenciais e política separadas das do aplicativo. No Compose principal, `sqs-gateway` é o único endpoint SQS publicado. Ele verifica assinatura SigV4, data e hash do corpo antes de aplicar os templates de menor privilégio de `deploy/aws/`; só então encaminha a operação ao LocalStack, que permanece em uma rede interna. A identidade de testes `test/test` pode operar filas isoladas, mas é bloqueada nas três filas da aplicação; `wager-test-app-local` acessa filas isoladas e apenas as ações do app na saída/DLQ, sem acesso à entrada financeira. O teste `TestMainSQSRejectsUnauthorizedFinancialMessage` executa o fluxo financeiro real e verifica que chave desconhecida, segredo errado e identidades sem permissão não alteram saldo, ledger, transações ou inbox. Esse gateway local cobre a ausência de [enforcement de IAM por padrão no LocalStack](https://docs.localstack.cloud/aws/developer-tools/security-testing/iam-policy-enforcement/), preservando FIFO e DLQ do broker usado pela aplicação.

Em AWS, o gateway local não participa do fluxo. Com endpoint e credenciais explícitas vazios, o adaptador usa a cadeia padrão de credenciais do SDK e o endpoint AWS da região; as políticas em `deploy/aws/` devem ser atribuídas a roles IAM distintas. `TestAWSIAMQueuePermissions` é uma verificação opcional de acesso efetivo na conta alvo, incluindo queue policies e demais controles da conta.

### Composição e encerramento

Uber Fx reúne módulos de configuração, logger, métricas, autenticação, PostgreSQL, casos de uso, HTTP, consumidor SQS e workers. `fx.Provide` liga construtores e `fx.Invoke` materializa componentes que devem iniciar mesmo sem uma requisição. Isso deixa o domínio sem dependência de Fx e centraliza a ordem de inicialização, validação das dependências e fechamento. Cada servidor ou worker registra `OnStart`/`OnStop` no `fx.Lifecycle`.

Os hooks de parada rodam em ordem inversa: HTTP reduz readiness e deixa de aceitar novas requisições; o consumidor cancela polling antes de aguardar mensagens já recebidas; workers de referências e outbox param; o pool fecha por último. O consumidor tenta concluir transação e delete no prazo; se o prazo vence, cancela o contexto de trabalho e libera a visibility para outra instância. Transações não confirmadas fazem rollback; claims de referências e outbox voltam a ser elegíveis ao expirar o lease. O orçamento padrão do Fx é 95 segundos, e o Compose concede 100 segundos antes de forçar a parada.

## Limitações / decisões de escopo

- O SQS Gateway do Compose verifica SigV4 e aplica o subconjunto de ações e filas usado neste desafio. Ele não reproduz toda a semântica de IAM, queue policies e avaliação de permissões da AWS; em produção, as roles e políticas em `deploy/aws/` precisam ser configuradas no serviço real.
- `FAILED` existe na máquina de estados para representar uma falha de infraestrutura comprovadamente permanente, mas nenhum fluxo operacional atual a persiste. Falhas transitórias seguem retry HTTP/SQS ou a agenda durável; mensagens não resolvidas podem ir à DLQ sem alterar artificialmente o estado financeiro.
- A fila de entrada recebe mensagens de um serviço interno confiável; `providerId` no envelope é dado de negócio, não uma credencial do produtor. O gateway e a política do broker controlam quem pode publicar no ambiente local.
