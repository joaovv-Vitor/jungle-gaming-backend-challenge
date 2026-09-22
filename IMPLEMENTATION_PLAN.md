# Plano de Implementação — Processamento Distribuído de Apostas em Go

## 1. Objetivo do documento

Este documento organiza a implementação do desafio em uma sequência executável, priorizando primeiro os requisitos eliminatórios e as garantias financeiras. Ele deve servir como:

- guia de arquitetura;
- backlog técnico;
- checklist de implementação;
- referência para testes e critérios de aceite;
- base para o futuro `ARCHITECTURE.md` e para a documentação operacional do `README.md`.

O sistema deverá receber operações financeiras por HTTP e AWS SQS, processá-las com as mesmas garantias e manter o resultado correto diante de concorrência, duplicidade, reinicializações e falhas parciais.

### 1.1. Parecer da revisão e escopo

Revisão baseada em `teste tecnoco.md`, seções 1–15. O plano original é coerente com a arquitetura e a maioria dos requisitos, mas precisava tornar executáveis as decisões sobre locks, idempotência, referências, recuperação e proteção no banco. Esta revisão resolve essas ambiguidades e inclui rastreabilidade na seção 24.

No início da revisão, o repositório não continha implementação Go, migrations ou ambiente executável. O estado atual está registrado abaixo. Os critérios das fases futuras são compromissos de implementação e verificação, não garantias já demonstradas. O enunciado é a fonte de requisitos; políticas adicionais estão identificadas como decisões do projeto. Não adicionar serviços, abstrações ou diferenciais sem necessidade para cumprir o desafio.

### 1.2. Estado da execução

Atualizado em 22 de setembro de 2026:

- Fase 0 concluída: decisões iniciais registradas em `ARCHITECTURE.md` e matriz de rastreabilidade definida neste plano.
- Fase 1 implementada: módulo Go, Fx, configuração, logger JSON, HTTP, health checks, graceful shutdown, Dockerfile, Compose e `.env.example`.
- Verificações da Fase 1 concluídas: `go test ./...`, `go test -race ./...`, `go vet ./...`, teste real de startup/health/shutdown, `docker compose config` e `docker compose up --build` com container saudável.
- Fase 1 aceita: imagem multi-stage construída, liveness/readiness validados pela porta publicada e graceful shutdown confirmado após `SIGTERM`.
- Fase 2 concluída: `Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry` e os quatro eventos tipados implementados com criação e reidratação separadas e erros classificáveis.
- Verificações da Fase 2 concluídas: regras de tipos e estados, zero values inválidos, saldo não negativo, moedas incompatíveis, equações do ledger, snapshots de eventos e overflow monetário.
- Fase 3 concluída: PostgreSQL 18, roles, migrations `up/down`, constraints, índices, triggers, `pgxpool`, unidade de trabalho e repositórios financeiros implementados.
- Verificações da Fase 3 concluídas em PostgreSQL real: níveis de isolamento, abertura e débito atômicos, reidratação, rollback, lock de carteira, escrita otimista, saldo não negativo, unicidade, imutabilidade e semântica do ledger, resultado histórico e agenda de referências pendentes.
- Fase 4 concluída: Keycloak provisionado, validação OIDC/JWT, roles `internal`/`provider`, abertura e consulta de carteira e ledger paginado implementados.
- Verificações da Fase 4 concluídas: token ausente, inválido e expirado, provider impedido de operar carteiras, abertura positiva atômica, conflito persistente, abertura zero sem registros financeiros e consulta autenticada do ledger.
- Fase 5 concluída: processamento financeiro dos cinco tipos externos, hash canônico, idempotência persistente, locks por carteira e endpoints autenticados de envio e consulta.
- Verificações da Fase 5 concluídas: vetor SHA-256 estável, regras e referências dos cinco tipos, replay histórico e conflitos, isolamento entre providers, 50 entregas concorrentes distribuídas por três serviços/pools com efeito único, disputa de duas apostas de `80.00` sobre `100.00` por conexões independentes e fluxo HTTP real com Keycloak e PostgreSQL.
- Fase 6 concluída: LocalStack, filas FIFO/DLQ, consumidor com long polling, backpressure por capacidade disponível e inbox transacional estão integrados ao mesmo caso de uso financeiro do HTTP.
- Verificações da Fase 6 concluídas: hash canônico do envelope, inbox e efeito financeiro no mesmo commit, reentrega com efeito único, conflito de hash, cruzamento HTTP/SQS, rollback da inbox, readiness do SQS, consumo/deleção reais e redrive automatizado no LocalStack. A falha entre commit e delete também possui teste automatizado, que comprova o replay seguro na entrega seguinte.
- Métricas iniciais da Fase 6 implementadas: recebimentos distinguem primeira entrega e reentrega pelo `ApproximateReceiveCount`; tentativas registram resultado e duração com labels de cardinalidade limitada, expostas em `/metrics` por um registry isolado.
- Concorrência da Fase 6 validada com três consumidores SQS e pools PostgreSQL independentes, barreira explícita de início, grupos FIFO distintos e reentregas posteriores; cada efeito financeiro permaneceu único.
- Fase 7 concluída: worker concorrente de referências pendentes com agenda e lease no PostgreSQL, backoff com jitter, TTL, reavaliação sob lock da carteira, resultado financeiro e eventos atômicos.
- Verificações da Fase 7 concluídas em PostgreSQL real: resolução após chegada tardia, referência ainda pendente, referência rejeitada/incompatível, expiração, lease assumido por outro pool e token obsoleto incapaz de alterar a agenda. O bootstrap completo iniciou dois workers e respondeu ao readiness.
- Fase 8 concluída: publisher concorrente da outbox, reivindicação por `FOR UPDATE SKIP LOCKED`, lease e token persistidos, retry com backoff e jitter, envio FIFO com `eventId` estável e confirmação condicional no PostgreSQL.
- Verificações da Fase 8 concluídas: dois publishers/pools disputando eventos, recuperação de lease abandonado, rejeição de token antigo, falha antes do envio, falha após envio e antes da confirmação, reenvio com o mesmo `eventId` e publicação real na fila FIFO do LocalStack.
- Fase 9 implementada: endpoint interno de reconciliação em snapshot somente leitura, cálculo monetário sem perda de precisão, detecção de overflow e divergências, logs de correlação e métricas de transações, workers, filas, readiness e shutdown.
- Verificações da Fase 9 incluem saldo com e sem lançamentos, diferença negativa, overflow, visibilidade antes/depois de commit financeiro, autorização da rota, recuperação do readiness e labels de métricas limitados.
- Fase 10 em andamento: teste de integração sobe três processos reais do binário com PIDs, portas e pools distintos; confirma a disputa de duas apostas, 50 entregas HTTP idênticas, progresso de uma carteira independente enquanto outra aguarda um lock observado no PostgreSQL, encerramento por SIGTERM e replay após reinício completo. A verificação final compara saldo, versão, ledger e contagem de eventos da outbox.
- Próxima etapa: completar a matriz de falhas temporárias de PostgreSQL/SQS, revisar timeouts e planos de consulta/índices antes da documentação de entrega.

---

## 2. Resultado esperado

Ao final, a solução deverá possuir:

- API HTTP autenticada;
- consumidor SQS autenticado no broker;
- PostgreSQL como fonte da verdade;
- carteiras com saldo e versão;
- ledger financeiro append-only;
- idempotência persistente compartilhada entre HTTP e SQS;
- processamento concorrente seguro por carteira;
- inbox transacional para mensagens recebidas;
- outbox transacional para eventos publicados;
- worker durável para referências pendentes;
- reconciliação entre saldo e ledger;
- logs, métricas e health checks;
- execução local completa com Docker Compose;
- testes unitários, de integração, concorrência e recuperação;
- documentação suficiente para reproduzir todos os cenários a partir de um checkout limpo.

---

## 3. Requisitos que não podem falhar

Os seguintes itens são eliminatórios e orientam a ordem de implementação:

1. Autenticação e autorização efetivas em todos os endpoints de negócio.
2. Nenhum valor monetário pode passar por `float32` ou `float64`.
3. O saldo nunca pode ficar negativo, inclusive com várias instâncias.
4. A mesma operação nunca pode gerar duas movimentações financeiras.
5. Idempotência deve ser persistente e sobreviver ao reinício de toda a aplicação.
6. A correção não pode depender de memória local ou da deduplicação do SQS FIFO.
7. Saldo, transação, ledger, inbox e outbox devem ser confirmados atomicamente quando aplicável.
8. Eventos externos só podem ser publicados depois do commit que os originou.
9. O ledger deve ser auditável, imutável e append-only.
10. PostgreSQL, SQS e IdP reais não podem ser integralmente substituídos por mocks nos testes de integração.

---

## 4. Princípios arquiteturais

### 4.1. PostgreSQL como fonte da verdade

Locks locais, cache, ordenação FIFO e memória do processo não serão usados para garantir integridade. Constraints, índices únicos, transações e locks no PostgreSQL protegerão as invariantes entre processos.

### 4.2. Uma regra de negócio, múltiplos adaptadores

HTTP, SQS e o worker de referências utilizarão o mesmo caso de uso transacional. Cada adaptador ficará responsável apenas por:

- autenticação ou credenciais de transporte;
- parsing e validação estrutural;
- criação do contexto de correlação;
- conversão de erros e respostas;
- confirmação ou liberação do transporte.

### 4.3. Domínio independente

Os pacotes de domínio não importarão:

- Uber Fx;
- HTTP;
- AWS SDK;
- PostgreSQL ou pgx;
- Keycloak/OIDC;
- bibliotecas de métricas ou logs.

### 4.4. Atomicidade financeira

Uma movimentação bem-sucedida deverá confirmar no mesmo commit:

- estado da `WagerTransaction`;
- saldo e versão da carteira;
- lançamento do ledger;
- eventos da outbox;
- conclusão da inbox, quando a origem for SQS.

### 4.5. Processamento at-least-once

Tanto mensagens recebidas quanto eventos publicados podem aparecer mais de uma vez. O sistema garantirá efeito financeiro exactly-once por meio de persistência, mas o transporte continuará sendo at-least-once.

---

## 5. Arquitetura proposta

```text
                         +-------------------+
                         |     Keycloak      |
                         +---------+---------+
                                   |
                                   v
+---------------+          +-------+--------+
| Cliente HTTP  +--------->|    HTTP API    |
+---------------+          +-------+--------+
                                   |
                                   v
                         +---------+----------+
                         | Application Service|
                         +---------+----------+
                                   |
                                   v
+---------------+          +-------+--------+          +----------------+
| SQS de entrada+--------->| SQS Consumer   |--------->|                |
+---------------+          +----------------+          |   PostgreSQL   |
                                                       |                |
+------------------+      +------------------+          | wallets        |
| Reference Worker +----->| mesmo caso de uso|--------->| transactions   |
+------------------+      +------------------+          | ledger         |
                                                       | inbox          |
+------------------+                                   | outbox         |
| Outbox Publisher +---------------------------------->|                |
+--------+---------+                                   +----------------+
         |
         v
+----------------+
| SQS de eventos |
+----------------+
```

### 5.1. Processo e composição

Será criado um único binário Go, composto por Uber Fx. API e workers poderão ser habilitados por configuração, permitindo:

- execução de todos os componentes em uma instância local;
- execução de múltiplas réplicas idênticas;
- testes que sobem apenas consumidores ou publishers;
- desligamento coordenado pelo `fx.Lifecycle`.

### 5.2. Dependências sugeridas

- Go Modules;
- Uber Fx para composição e lifecycle;
- `pgx` para acesso explícito ao PostgreSQL;
- AWS SDK for Go para SQS;
- uma biblioteca OIDC/JWT compatível com descoberta e JWKS;
- `net/http` para o servidor e, inicialmente, para roteamento;
- Prometheus client para métricas;
- logger estruturado em JSON.

As versões exatas deverão ser fixadas no `go.mod`, Dockerfile e documentação.

---

## 6. Estrutura inicial de diretórios

```text
.
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── domain/
│   │   ├── money/
│   │   ├── wallet/
│   │   ├── wagering/
│   │   ├── ledger/
│   │   └── event/
│   ├── application/
│   │   ├── wallet/
│   │   ├── wagering/
│   │   └── reconciliation/
│   ├── ports/
│   ├── adapters/
│   │   ├── postgres/
│   │   ├── http/
│   │   ├── sqs/
│   │   ├── oidc/
│   │   └── observability/
│   ├── platform/
│   │   ├── config/
│   │   ├── clock/
│   │   └── id/
│   └── bootstrap/
├── migrations/
├── deploy/
│   ├── keycloak/
│   └── localstack/
├── tests/
│   ├── integration/
│   ├── concurrency/
│   └── recovery/
├── .env.example
├── docker-compose.yml
├── Dockerfile
├── go.mod
├── go.sum
├── ARCHITECTURE.md
└── README.md
```

Essa estrutura poderá ser simplificada se algum pacote ficar artificialmente pequeno. O objetivo é separar domínio, aplicação e infraestrutura sem criar abstrações sem uso real.

---

## 7. Modelagem do domínio

## 7.1. Money

Representação proposta:

- `int64` em unidades mínimas, isto é, centavos;
- moeda armazenada junto ao valor;
- escala fixa de duas casas;
- código de moeda validado como ISO 4217 no formato de três letras maiúsculas;
- cenários principais restritos a BRL, mantendo o tipo preparado para validar moedas incompatíveis.

Formato de três letras não comprova pertencimento à ISO 4217: usar uma lista explícita das moedas suportadas (BRL e ao menos uma segunda moeda válida nos testes). O limite positivo com `int64` e escala 2 é `92233720368547758.07`; o limite interno negativo é `-92233720368547758.08`, cuja negação deve falhar por overflow. O zero value de `Money`, sem moeda, é inválido; `Zero(currency)` é válido. Nenhum DTO ou driver pode converter dinheiro para ponto flutuante.

Operações obrigatórias:

- parsing de string decimal;
- criação de zero por moeda;
- soma;
- subtração;
- negação;
- comparação;
- serialização para duas casas decimais.

Validações:

- rejeitar vazio;
- rejeitar sinal negativo em entradas financeiras externas;
- rejeitar `NaN` e `Infinity`;
- rejeitar notação científica;
- rejeitar mais de duas casas decimais;
- rejeitar moeda vazia ou inválida;
- detectar overflow no parsing e em toda operação aritmética;
- rejeitar operações entre moedas diferentes.

Decisão sugerida para reduzir ambiguidades: exigir externamente valores com exatamente duas casas decimais. Assim, `25.00` é válido e `25`, `25.0` ou `25.000` são inválidos. Essa decisão deverá constar no contrato.

## 7.2. Wallet

Estado:

- ID;
- player ID;
- moeda;
- saldo;
- versão;
- criação e atualização.

Comportamentos:

- criar;
- reidratar;
- debitar;
- creditar.

Invariantes:

- saldo nunca negativo;
- moeda da operação igual à moeda da carteira;
- versão inicial igual a 1;
- após a criação, a versão só aumenta quando o saldo muda;
- `LOSS` não altera saldo nem versão;
- abertura com saldo positivo cria `OPENING`, mas a carteira recém-criada continua na versão 1.

## 7.3. WagerTransaction

Tipos:

- `OPENING`;
- `BET`;
- `WIN`;
- `LOSS`;
- `REFUND`;
- `ROLLBACK`.

Estados:

- `PENDING`;
- `PENDING_REFERENCE`;
- `PROCESSED`;
- `REJECTED`;
- `FAILED`.

Regras de transição:

- toda operação externa começa em `PENDING` dentro da transação SQL;
- uma operação sem dependência não precisa ter `PENDING` confirmado separadamente;
- referência ausente leva a `PENDING_REFERENCE`;
- sucesso leva a `PROCESSED`;
- regra de negócio definitiva leva a `REJECTED`;
- falha permanente de infraestrutura auditável leva a `FAILED`;
- estados terminais nunca sofrem nova transição;
- reidratação não emite eventos nem repete movimentações.

Máquina de estados adotada: `PENDING → PROCESSED | REJECTED | PENDING_REFERENCE | FAILED` e `PENDING_REFERENCE → PROCESSED | REJECTED | FAILED`. Reagendar uma referência mantém seu estado e não emite novamente o evento de pendência. Estados terminais têm resultado imutável.

Não haverá aceite assíncrono em `PENDING`: ele só existe dentro da transação que conclui a operação ou confirma `PENDING_REFERENCE`. Falhas transitórias provocam rollback e retry, nunca `FAILED`. Este último fica reservado a uma falha de infraestrutura comprovadamente permanente ao retomar trabalho já persistido; registrar código e diagnóstico seguro em commit separado, sem efeitos financeiros. Indisponibilidade, timeout, commit de resultado desconhecido e erro de programação não devem ser convertidos automaticamente em `FAILED`.

Entidades devem usar campos não exportados, construtores validados e erros classificáveis com `errors.Is`/`errors.As`. Rejeitar valores não inicializados, sem `panic` para erros de negócio. Toda porta de I/O recebe `context.Context`; reidratação valida os dados sem reaplicar transições.

## 7.4. WalletLedgerEntry

Cada registro conterá:

- ID;
- wallet ID;
- transaction ID;
- direção `DEBIT` ou `CREDIT`;
- valor;
- saldo anterior;
- saldo posterior;
- instante de criação.

O construtor validará:

- crédito: `balanceAfter = balanceBefore + money`;
- débito: `balanceAfter = balanceBefore - money`;
- valor positivo;
- moedas compatíveis;
- saldos não negativos.

## 7.5. Eventos de domínio/integração

Eventos obrigatórios:

- `WagerTransactionProcessed`;
- `WagerTransactionRejected`;
- `WalletBalanceChanged`;
- `WagerTransactionPendingReference`.

Envelope comum:

- `eventId`;
- `eventType`;
- `aggregateId`;
- `correlationId`;
- `causationId`, quando disponível;
- `occurredAt`;
- `version`;
- `data` tipado.

O evento deverá ser construído como snapshot imutável antes da gravação na outbox.

Tipo e versão são definidos pelo construtor, não pelo chamador. Usar UTC/RFC 3339 e dinheiro em strings. `WalletBalanceChanged.data` contém obrigatoriamente `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter` e `walletVersion`. Os demais eventos carregam a identidade da transação, estado e os dados pertinentes ao resultado; rejeição inclui `failureCode`, pendência inclui a referência esperada. Eventos de `OPENING` omitem metadados externos inaplicáveis. A versão do envelope é a versão do contrato, distinta de `walletVersion`.

---

## 8. Regras das operações financeiras

| Tipo | Efeito | Condições principais |
| --- | --- | --- |
| `BET` | Débito | Valor positivo e saldo suficiente |
| `WIN` | Crédito | Valor positivo; referência de aposta opcional |
| `LOSS` | Nenhum | Valor exatamente zero |
| `REFUND` | Crédito | Referência obrigatória a `BET` processada |
| `ROLLBACK` | Oposto da referência | Referência obrigatória a `BET`, `WIN` ou `REFUND` processada |

Para qualquer referência, validar:

- mesmo provider;
- mesmo jogador;
- mesma carteira;
- mesma moeda;
- mesma rodada;
- mesmo valor para reversões;
- tipo referenciado permitido;
- referência processada com sucesso.

## 8.1. Política proposta para reversões

Para impedir créditos duplicados e manter uma regra explicável:

- uma `BET` pode ter uma única reversão direta bem-sucedida: `REFUND` ou `ROLLBACK`;
- uma `WIN` pode ter um único `ROLLBACK`;
- um `REFUND` pode ter um único `ROLLBACK`;
- nenhuma referência pode receber duas reversões processadas;
- operações rejeitadas não consomem a possibilidade de reversão;
- o controle será protegido pelo lock da carteira à qual a referência pertence e por índice único parcial no banco.

Essa política é deliberadamente conservadora e deverá ser registrada em `ARCHITECTURE.md`.

Exemplo: `BET(25) → REFUND(25) → ROLLBACK do REFUND(25)` resulta em débito líquido de 25. A aposta original continua com sua reversão direta consumida; outro `REFUND` ou `ROLLBACK` dela é rejeitado. Não se reabre essa possibilidade ao desfazer o refund. O estado da operação original continua `PROCESSED`; a compensação é outra transação e outro lançamento.

### 8.1.1. Referências e validação anterior à espera

- `WIN` sem referência credita normalmente. Com referência, exigir `BET` processada da mesma identidade financeira e rodada; o prêmio pode diferir do valor apostado. Referência indisponível segue o mesmo fluxo durável das reversões.
- `BET` e `LOSS` com referência são entradas inválidas; `OPENING` nunca aceita referência externa.
- Validar carteira existente, jogador, moeda, tipo, valor e autorreferência antes de persistir uma espera. `referenceExternalTransactionId == externalTransactionId` resulta em `INVALID_REFERENCE`.
- Referência existente com identidade ou tipo incompatível é rejeitada imediatamente. Referência válida em `PENDING`/`PENDING_REFERENCE` aguarda; terminal `REJECTED`/`FAILED` gera `REFERENCE_NOT_PROCESSED`.
- Ciclos de dependência não podem esperar indefinidamente: regras de tipos e autorreferência eliminam os casos evidentes; a expiração persistida limita os demais.
- Ao atingir o TTL sem referência utilizável, usar `REFERENCE_NOT_FOUND`, com motivo seguro distinguindo ausência de referência de referência ainda pendente. O prazo não reinicia em replay ou restart.

## 8.2. Códigos de falha propostos

- `BET_INSUFFICIENT_FUNDS`;
- `REVERSAL_INSUFFICIENT_FUNDS`;
- `REFERENCE_NOT_FOUND`;
- `REFERENCE_NOT_PROCESSED`;
- `REFERENCE_MISMATCH`;
- `REFERENCE_TYPE_NOT_ALLOWED`;
- `ALREADY_REVERSED`;
- `CURRENCY_MISMATCH`;
- `INVALID_OPERATION_AMOUNT`;
- `WALLET_PLAYER_MISMATCH`.

Acrescentar `INVALID_REFERENCE`, `MONEY_OVERFLOW`, `IDEMPOTENCY_CONFLICT`, `EXTERNAL_TRANSACTION_CONFLICT`, `WALLET_NOT_FOUND` e `INFRASTRUCTURE_PERMANENT_FAILURE`. Conflitos de identidade e carteira inexistente antes do aceite não criam transação de negócio; falha permanente usa `FAILED`. Overflow de entrada é erro de contrato; overflow ao aplicar crédito válido é rejeição persistida sem mudança de saldo. Documentar catálogo com código, HTTP, possibilidade de retry e persistência. `REJECTED` é definitivo para a identidade já aceita; alterações de conteúdo exigem outra identidade externa e chave. Falhas corrigíveis de parsing/autorização não consomem a chave.

Erros de parsing ou contrato não precisam gerar uma `WagerTransaction`. Rejeições ocorridas após a operação ser aceita e identificada serão persistidas para permitir replay fiel.

---

## 9. Modelo relacional proposto

## 9.1. Tabela `wallets`

Campos principais:

- `id UUID PRIMARY KEY`;
- `player_id UUID NOT NULL`;
- `currency CHAR(3) NOT NULL`;
- `balance_minor BIGINT NOT NULL`;
- `version BIGINT NOT NULL`;
- `created_at TIMESTAMPTZ NOT NULL`;
- `updated_at TIMESTAMPTZ NOT NULL`.

Proteções:

- `UNIQUE(player_id, currency)`;
- `CHECK(balance_minor >= 0)`;
- `CHECK(version >= 1)`;
- moeda em formato válido.

Identidade, jogador e moeda da carteira são imutáveis. Usar uma role de migrations separada da role de runtime, sem privilégios de proprietário ou superusuário para a aplicação.

## 9.2. Tabela `wager_transactions`

Campos principais:

- identidade interna;
- origem interna ou externa;
- provider;
- external transaction ID;
- idempotency key;
- payload hash;
- wallet e player;
- round e game;
- kind e status;
- amount minor e currency;
- referência externa e interna;
- failure code;
- saldo resultante da operação;
- tentativa de referência, `next_attempt_at` e `expires_at`;
- timestamps.

Proteções:

- `UNIQUE(provider_id, idempotency_key)` para operações externas;
- `UNIQUE(provider_id, external_transaction_id)`;
- uma única abertura por carteira;
- checks que diferenciem origem interna e externa;
- checks de campos obrigatórios por tipo;
- checks de estados e tipos;
- índice de pendências por `status` e `next_attempt_at`;
- índice único parcial impedindo mais de uma reversão processada da mesma referência.

Detalhamento obrigatório das migrations:

- `origin = INTERNAL` implica `kind = OPENING`, identidade interna estável e campos externos nulos; `origin = EXTERNAL` exige metadados externos não vazios e proíbe `OPENING`.
- `OPENING` exige valor positivo, estado `PROCESSED` e unicidade parcial por `wallet_id`; abertura zero não insere transação.
- `LOSS` exige valor zero; os demais tipos exigem valor positivo. Referências externas são obrigatórias para reversões; uma reversão processada exige referência interna resolvida.
- Índice único em `reference_transaction_id` filtrado por `status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK')`, implementando a política conservadora da seção 8.1.
- FK para carteira; FK composta do ledger para transação/carteira/moeda, com chave candidata correspondente. Referência resolvida usa FK e validação transacional de provedor, jogador, moeda, carteira e rodada.
- Checks coerentes para `failure_code`, resultado terminal, tipos e estados, acompanhados de proteção contra alteração de identidade/hash e de resultado terminal.
- Campos `reference_attempts`, `next_attempt_at`, `expires_at`, `lease_token` e `locked_until` para pendências; índices parciais para trabalho elegível. Usar o relógio do banco para agenda e leases.

Checks validam a própria linha; não usá-los como se verificassem outras tabelas. Relações entre registros exigem FKs, índices únicos ou triggers específicos, além da transação da aplicação.

O saldo retornado em sucesso ou rejeição de negócio será persistido. Seu replay devolverá esse saldo histórico, e não o saldo atual da carteira. `FAILED` sem resultado financeiro não inventa saldo de processamento.

## 9.3. Tabela `wallet_ledger_entries`

Proteções:

- `UNIQUE(wallet_id, transaction_id)`;
- amount maior que zero;
- moeda consistente;
- expressão de saldo compatível com a direção;
- trigger que rejeita `UPDATE`;
- trigger que rejeita `DELETE`.

Schema explícito: IDs com FKs, direção restrita, moeda, `amount_minor`, `balance_before_minor`, `balance_after_minor`, `wallet_version` e `created_at`. Validar saldos não negativos e a equação em aritmética exata, sem overflow intermediário. `wallet_version` permite percorrer o histórico na ordem financeira e não apenas pela hora.

A role da aplicação recebe somente `SELECT`/`INSERT` no ledger, sem `UPDATE`, `DELETE`, `TRUNCATE` ou poder de desabilitar triggers; bloquear também `TRUNCATE` por trigger. Evitar exclusões em cascata. Testar essas proteções usando a mesma role do runtime.

Para sustentar a garantia de integridade no banco, implementar constraint triggers diferíveis que validem no commit a correspondência entre alteração de saldo/versão e lançamento, bem como transação processada, valor e direção. `LOSS`, rejeições e abertura zero não admitem ledger. Não executar soma integral do histórico a cada operação: verificar apenas carteira/transações afetadas; reconciliação faz a conferência completa. Escritas diretas inválidas devem falhar nos testes SQL.

## 9.4. Tabela `consumer_inbox`

Campos:

- consumer name;
- message ID;
- payload hash;
- received at;
- completed at;
- transaction ID associada, quando disponível.

Proteção:

- `UNIQUE(consumer_name, message_id)`.

## 9.5. Tabela `outbox_events`

Campos:

- event ID;
- aggregate ID;
- event type e version;
- correlation e causation IDs;
- payload JSONB;
- occurred at;
- attempts;
- next attempt at;
- locked by e locked until;
- lease token novo a cada reivindicação;
- published at;
- last error resumido.

Índices:

- eventos não publicados prontos para envio;
- leases expirados;
- aggregate ID para auditoria.

Payload, identidade, tipo e versão do evento são imutáveis após inserção; somente metadados de entrega podem mudar. Proteger esse limite por privilégios de coluna ou trigger.

---

## 10. Idempotência

## 10.1. Identidades persistentes

Duas unicidades independentes serão protegidas:

1. `(providerId, idempotencyKey)`;
2. `(providerId, externalTransactionId)`.

Isso impede que o mesmo evento financeiro seja reaplicado usando outra chave.

## 10.2. Hash canônico

O hash incluirá apenas campos de negócio:

- provider ID;
- external transaction ID;
- player ID;
- wallet ID;
- round ID;
- game ID;
- kind;
- money amount;
- money currency;
- reference external transaction ID, quando existente.

Não incluirá:

- idempotency key;
- headers HTTP;
- message ID;
- timestamps e metadados do transporte;
- correlation ID.

Procedimento:

1. Validar e normalizar os valores permitidos: dinheiro com exatamente duas casas, UUIDs em formato textual canônico, moeda suportada em maiúsculas; rejeitar espaços periféricos em identificadores em vez de removê-los silenciosamente.
2. Construir uma representação JSON canônica com chaves ordenadas.
3. Calcular SHA-256.
4. Persistir o hash junto à transação.

Ordenar chaves também em objetos aninhados, como `money`; definir escaping UTF-8 e omissão da referência ausente. Rejeitar campos desconhecidos, chaves JSON repetidas, referência vazia/nula e conteúdo extra após o objeto. Não usar serialização de struct em ordem de declaração como definição de JSON canônico. Criar vetores de teste com bytes canônicos e SHA-256 esperado, equivalentes entre os adaptadores. Identificadores de provedor, rodada, jogo e operação são sensíveis a maiúsculas/minúsculas.

Comportamentos:

- mesma chave e mesmo hash: replay;
- mesma chave e hash diferente: conflito;
- mesmo external ID com outra chave: `409 EXTERNAL_TRANSACTION_CONFLICT`, mesmo com hash equivalente; decisão explícita para não criar aliases de chave;
- replay terminal: retornar status, failure code quando aplicável e saldo histórico quando houver resultado financeiro persistido;
- replay pendente: retornar o estado pendente atual.

Consultar ambas as identidades e autorizar antes de retornar qualquer resultado. Se a chave aponta para uma transação e o ID externo para outra, retornar conflito. Usar inserção com conflito tratado sem deixar a transação SQL abortada; após corrida, reler o vencedor em uma nova instrução sob `READ COMMITTED`. Nunca fazer upsert que sobrescreva o hash, a chave original ou o resultado existente. Timeout de commit tem resultado desconhecido: consultar/repetir com a mesma identidade, sem compensar nem registrar rejeição por suposição.

---

## 11. Concorrência e transação SQL

## 11.1. Estratégia principal

Será usado locking pessimista por carteira:

```sql
SELECT ... FROM wallets WHERE id = $1 FOR NO KEY UPDATE;
```

Consequências:

- operações da mesma carteira são serializadas entre processos;
- carteiras diferentes avançam em paralelo;
- não existe mutex global;
- lost updates são evitados;
- saldo e versão são calculados sobre o estado confirmado mais recente.

Usar `READ COMMITTED`, uma operação financeira por transação e bloqueio antes de ler/calcular o saldo. `FOR NO KEY UPDATE` serializa escritores de saldo sem exigir alteração de chaves; carteira, jogador e moeda não são alteráveis. O SQL de atualização também verifica saldo não negativo e quantidade esperada de linhas afetadas. Não fazer chamadas ao IdP/SQS dentro da transação.

## 11.2. Ordem de locks

Para reduzir deadlocks, todos os caminhos financeiros deverão adotar uma ordem estável. Proposta:

1. no SQS, inserir/deduplicar a inbox antes de qualquer lock financeiro;
2. consultar identidades existentes para replay/conflito, sem lock de escrita antecipado na transação de negócio;
3. bloquear a carteira alvo e validar jogador/moeda;
4. inserir a identidade externa ou bloquear a operação pendente existente e revalidar seu estado/lease;
5. consultar a referência e validar identidade e reversões; referências válidas pertencem à carteira já bloqueada, sem adquirir lock de outra carteira;
6. aplicar a operação e persistir ledger, eventos, resultado e conclusão da inbox.

O worker de referências deverá buscar candidatos sem manter locks longos e processar cada item usando a mesma ordem do caso de uso principal.

A reivindicação do job ocorre em transação curta separada e termina antes de adquirir o lock da carteira. Não manter lock na operação enquanto se espera pela carteira: isso inverteria a ordem adotada pelo HTTP. Validar estado e token do lease novamente após bloquear a carteira. Retentar a transação inteira em deadlock ou falha de serialização com limite e jitter; ao esgotar, responder indisponibilidade transitória ou permitir reentrega, sem rejeição financeira.

## 11.3. Cenário obrigatório de disputa

Estado inicial: `100.00 BRL`.

Duas instâncias recebem apostas diferentes de `80.00 BRL`:

- a primeira que bloquear a carteira debita e confirma `20.00`;
- a segunda lê o saldo confirmado de `20.00` e registra rejeição por saldo insuficiente;
- saldo final: `20.00`;
- um único lançamento de débito;
- uma transação `PROCESSED`;
- uma transação `REJECTED`;
- replays não alteram o resultado.

---

## 12. Fluxos principais

## 12.1. Abertura de carteira

1. Autenticar como serviço interno.
2. Validar player e saldo inicial.
3. Iniciar transação SQL.
4. Inserir carteira com versão 1.
5. Se o saldo for zero, confirmar sem operação financeira.
6. Se positivo, criar `OPENING` processada.
7. Inserir crédito no ledger.
8. Inserir `WagerTransactionProcessed` e `WalletBalanceChanged` na outbox.
9. Commit.

Conflito de `(playerId, currency)` retorna `409`.

## 12.2. Operação HTTP

1. Validar token OIDC.
2. Extrair provider autorizado do token.
3. Conferir provider do corpo.
4. Validar `Idempotency-Key`.
5. Fazer parsing sem ponto flutuante.
6. Calcular hash canônico.
7. Iniciar transação SQL.
8. Consultar as duas identidades; se já existir, avaliar replay ou conflito.
9. Bloquear a carteira, validar jogador/moeda e registrar a identidade conforme a seção 11.2; reler o resultado em caso de corrida.
10. Resolver e validar referência, quando aplicável, sob a mesma coordenação por carteira.
11. Persistir `PENDING_REFERENCE`, agenda, expiração e evento apenas se a dependência válida ainda não estiver disponível.
12. Caso contrário, executar a regra financeira sobre a carteira já bloqueada.
13. Persistir estado, saldo, ledger e outbox.
14. Commit.
15. Mapear o resultado para HTTP.

## 12.3. Operação SQS

1. Receber a mensagem com long polling.
2. Validar envelope, tipo e origem autorizada conforme a política de produtores da seção 13.3.
3. Calcular hash do envelope para a inbox.
4. Iniciar transação SQL.
5. Inserir/deduplicar `(consumerName, messageId)`.
6. Se concluída com mesmo hash, confirmar a duplicata sem reprocessar.
7. Se o hash divergir, tratar como mensagem inválida/permanente.
8. Executar o mesmo caso de uso financeiro do HTTP utilizando a transação SQL já aberta, sem commit interno ou conexão independente.
9. Marcar inbox como concluída.
10. Commit.
11. Excluir a mensagem da fila somente depois do commit.

Se houver queda entre os passos 10 e 11, a reentrega encontrará o processamento persistido e não repetirá o efeito.

O hash da inbox é distinto do hash financeiro: SHA-256 do envelope canônico validado, incluindo `messageId`, `type`, `occurredAt` e `data`, excluindo atributos de entrega do broker. O envelope de uma reentrega deve manter esses campos. Outra mensagem com `messageId` novo passa pela inbox e é deduplicada pelas identidades financeiras. Inbox confirmada representa tratamento durável, inclusive transferência para `PENDING_REFERENCE` ou rejeição de negócio.

## 12.4. Referência pendente

1. Buscar operações `PENDING_REFERENCE` cujo `next_attempt_at` chegou, incluindo expiradas para finalização.
2. Reivindicar lote limitado com `SKIP LOCKED`, gravar lease/token e confirmar; processar cada item em nova transação na ordem da seção 11.2.
3. Verificar TTL e tentativas.
4. Se a referência não existir, incrementar tentativa e reagendar.
5. Se estiver pendente, reagendar.
6. Se terminou rejeitada ou failed, rejeitar a dependente com código estável.
7. Se estiver processada, executar a operação financeira.
8. Se o limite expirar, rejeitar com `REFERENCE_NOT_FOUND`.

Backoff sugerido: exponencial com jitter, limite máximo configurável e TTL persistido.

Decisão inicial: TTL de 24 horas, backoff de 1 segundo até 5 minutos, com jitter; usar TTL como limite de negócio e tentativas como observabilidade. O teste usa relógio/prazos controláveis. Reavaliar a referência sob lock antes da rejeição por expiração. Falha de infraestrutura provoca rollback/reagendamento, não consome uma tentativa de consulta bem-sucedida nem se transforma em referência inexistente. Leases expiram e atualizações exigem o token vigente. Um replay HTTP/SQS de pendência apenas consulta o estado; o worker assume sua continuação.

## 12.5. Publicação da outbox

1. Selecionar eventos disponíveis com `FOR UPDATE SKIP LOCKED`.
2. Registrar lease e commit da reivindicação.
3. Publicar no SQS de eventos.
4. Marcar `published_at` em nova transação, condicionando ao token do lease vigente.
5. Em erro, incrementar tentativa e reagendar com backoff, também condicionado ao token.
6. Em queda, outra instância assume o evento após expiração do lease.

Se a aplicação cair após publicar e antes de marcar, o evento poderá ser republicado com o mesmo `eventId`. Consumidores deverão deduplicá-lo por esse identificador.

Não descartar nem marcar como publicado após esgotamento de tentativas. Falhas persistentes continuam armazenadas, com alerta e retry em intervalo limitado; eventual quarentena precisa manter replay operacional. Não confundir DLQ do consumidor da fila de saída com falha do publisher ao enviar. O token protege a confirmação por um publisher atrasado, mas não impede envio externo duplicado após perda do lease.

Múltiplos publishers podem enviar eventos fora da ordem de commit, mesmo em fila FIFO. Não prometer ordem financeira global: consumidores deduplicam por `eventId` e usam `walletVersion` para detectar lacunas/reordenar quando necessário. O banco e a reconciliação permanecem a referência financeira.

## 12.6. Reconciliação

1. Autenticar como serviço interno.
2. Abrir transação `REPEATABLE READ` somente leitura.
3. Ler o saldo armazenado.
4. Somar créditos e débitos do ledger no mesmo snapshot.
5. Calcular `difference = storedBalance - calculatedBalance`.
6. Retornar o resultado sem alterar dados.
7. Em divergência, emitir log e incrementar métrica.

Usar `COALESCE` para histórico vazio e preservar a precisão da soma: `SUM(BIGINT)` retorna valor de precisão ampliada, que não deve ser convertido silenciosamente para `int64` ou float. Validar limites ao criar `Money`; em corrupção com total/diferença fora do intervalo, retornar erro estável `RECONCILIATION_OVERFLOW`, log e métrica, sem truncamento. `checkedEntries` inclui `OPENING`. Verificar também a continuidade de saldos/versões no histórico nos testes de integridade.

---

## 13. Autenticação e autorização

## 13.1. Keycloak

O Docker Compose deverá importar automaticamente:

- realm local;
- client da API;
- clients de providers para testes;
- client do serviço interno;
- roles/scopes necessários;
- claim que identifica o provider.

Fluxo principal: OAuth 2.0 `client_credentials`.

## 13.2. Perfis

### Provider

- envia operações de aposta;
- consulta somente suas próprias transações;
- não cria carteiras;
- não executa reconciliação;
- não acessa dados de outro provider por ID interno, path ou replay.

### Serviço interno

- cria carteiras;
- consulta carteiras e ledger;
- executa reconciliação;
- pode consultar operações para suporte operacional, se essa permissão for explicitamente concedida.

## 13.3. Regras de segurança

- `providerId` deve ser determinado pelo token;
- o valor recebido no corpo deve coincidir com o claim, mas nunca conceder autoridade;
- validar issuer, audience, assinatura, expiração e algoritmo;
- aplicar timeout e cache seguro de JWKS;
- não registrar tokens ou credenciais;
- falhas de autenticação/autorização não produzem qualquer registro financeiro;
- o broker deverá usar credenciais e políticas próprias, mesmo no ambiente local quando suportado.

Decisão de confiança para a fila compartilhada: apenas um serviço de ingestão interno confiável pode produzir nela, em nome dos provedores; provedores externos usam HTTP autenticado. A política do broker restringe envio ao produtor interno e recebimento/delete ao consumidor. `data.providerId` sozinho não autentica um provedor. Se envio direto por provedores for adicionado, será necessário vincular a identidade a uma origem verificável, como filas segregadas ou envelope assinado; essa extensão fica fora do escopo inicial.

Provisionar e demonstrar políticas de acesso do broker com as capacidades efetivamente disponíveis no emulador. Credenciais locais fictícias não comprovam enforcement de IAM: se o ambiente escolhido não negar acessos indevidos, registrar a limitação e fornecer evidência em ambiente que suporte a política, sem declarar o requisito validado apenas por existir um arquivo de policy.

---

## 14. Contratos HTTP propostos

## 14.1. Status codes

| Situação | Código sugerido |
| --- | ---: |
| Carteira criada | `201 Created` |
| Operação processada | `200 OK` |
| Replay de operação processada | `200 OK` |
| Replay de rejeição persistida | `422 Unprocessable Entity` |
| Falha permanente persistida (`FAILED`), inclusive replay | `500 Internal Server Error` |
| Referência pendente | `202 Accepted` |
| Entrada estruturalmente inválida | `400 Bad Request` |
| Token ausente, inválido ou expirado | `401 Unauthorized` |
| Identidade sem permissão | `403 Forbidden` |
| Recurso inexistente ou não visível | `404 Not Found` |
| Chave/hash ou identidade em conflito | `409 Conflict` |
| Rejeição de negócio persistida | `422 Unprocessable Entity` |
| Dependência temporariamente indisponível | `503 Service Unavailable` |

Todos os erros deverão usar um envelope estável com código, mensagem segura e correlation ID.

Contrato mínimo: sucesso retorna `transactionId`, `status`, `balance` histórico e `idempotentReplay`; rejeição persistida acrescenta `failureCode` e preserva o saldo observado; pendência retorna `transactionId`, `status`, referência e `idempotentReplay`, sem inventar saldo de processamento. `FAILED` informa `failureCode` sem saldo de sucesso. Erros anteriores ao aceite usam `{code, message, correlationId}`. `503` orienta retry com a mesma identidade e não afirma que um commit desconhecido falhou. GETs retornam `200` para transação visível em qualquer estado, incluindo seu código de falha. Replays preservam o resultado de negócio, mudando apenas `idempotentReplay` e metadados do transporte.

Limitar tamanho do body, comprimentos de identificadores, prazo de requisição e paginação. Em leituras, aplicar filtro de provedor na query; não carregar e expor resultado antes de autorizar. Health checks são públicos; `/metrics` deve usar acesso interno ou porta restrita.

## 14.2. Paginação do ledger

- ordenação estável por `(wallet_version, id)`, acompanhada de índice por carteira;
- cursor opaco codificado pela aplicação;
- limite padrão 50;
- limite máximo documentado;
- cursor inválido retorna erro de contrato;
- consultas subsequentes não devem repetir nem omitir registros dentro da ordenação definida.

Adotar paginação ascendente por chave, com limite máximo 200. Cursor contém carteira, última chave e a versão máxima capturada na primeira página; restringir páginas seguintes a esse limite. Assim créditos posteriores não mudam o conjunto em paginação. `wallet_version` é gravada sob lock; timestamps e UUIDs isolados não garantem ordem de commit. Validar vínculo do cursor com a carteira e versão do formato, além de sua codificação opaca.

---

## 15. SQS

Filas mínimas:

- `wager-transactions.fifo`;
- `wager-transactions-dlq.fifo`;
- `wager-events.fifo`;
- DLQ de eventos, se a política de publicação exigir uma fila separada.

Configurações a documentar:

- redrive policy;
- número máximo de recebimentos;
- visibility timeout;
- long polling;
- tamanho do batch;
- concorrência dos workers;
- backoff;
- retenção.

Identificadores sugeridos:

- `MessageGroupId`: wallet ID, permitindo ordenar operações da mesma carteira sem bloquear carteiras diferentes;
- `MessageDeduplicationId`: message ID na entrada e event ID na saída.

Essas propriedades melhoram o comportamento do transporte, mas não substituem inbox, idempotência, locks ou constraints.

Mensagens inválidas ou permanentemente impossíveis deverão permanecer sem confirmação até atingirem a política de redrive para a DLQ, com log e métrica adequados.

Valores iniciais do projeto: `maxReceiveCount = 5`, long polling de 20 segundos, visibility de 60 segundos, prazo de processamento de 20 segundos e shutdown de 30 segundos; tornar configuráveis e verificar sua compatibilidade no startup. Se o trabalho exceder a janela prevista, renovar visibilidade com antecedência; falha na renovação mantém o caminho idempotente. Aplicar backoff de reentrega sem espera ocupada. Validar limites dessas configurações na documentação da versão escolhida durante o bootstrap.

Rejeição de negócio persistida e `PENDING_REFERENCE` confirmado permitem delete; envelope inválido, conflito permanente de mensagem e falha transitória não permitem delete sem tratamento durável adequado. Em shutdown, só liberar visibilidade após interromper/encerrar o trabalho local; falha de delete após commit permite reentrega. Métrica de DLQ deve observar mensagens efetivamente disponíveis nela, não apenas erros que talvez sejam redirecionados.

---

## 16. Uber Fx e lifecycle

Módulos sugeridos:

- `ConfigModule`;
- `LoggingModule`;
- `MetricsModule`;
- `DatabaseModule`;
- `OIDCModule`;
- `RepositoryModule`;
- `ApplicationModule`;
- `HTTPModule`;
- `SQSConsumerModule`;
- `ReferenceWorkerModule`;
- `OutboxModule`;
- `HealthModule`.

No startup:

1. carregar e validar configuração;
2. criar logger e métricas;
3. conectar ao PostgreSQL;
4. validar recursos SQS e configuração OIDC;
5. iniciar servidor HTTP;
6. iniciar workers.

No shutdown:

1. marcar readiness como indisponível;
2. parar de aceitar novas requisições;
3. parar de buscar novas mensagens e jobs;
4. aguardar trabalhos em andamento dentro do prazo;
5. liberar visibility/leases quando não for possível concluir;
6. encerrar servidor;
7. fechar clientes SQS/OIDC;
8. fechar pool do PostgreSQL.

---

## 17. Observabilidade

## 17.1. Logs

Logs JSON com os identificadores disponíveis:

- correlation ID;
- message ID;
- transaction ID;
- wallet ID;
- provider ID;
- event ID;
- worker e instância.

Não registrar:

- tokens;
- client secrets;
- payload financeiro completo;
- dados desnecessários do jogador.

## 17.2. Métricas

- operações por status e tipo;
- latência de processamento;
- replays idempotentes;
- conflitos de hash;
- saldo insuficiente;
- conflitos/retries de concorrência;
- mensagens recebidas e redeliveries;
- mensagens destinadas à DLQ;
- tentativas de referências pendentes;
- tamanho e atraso da outbox;
- publicações e republicações;
- falhas por dependência;
- divergências de reconciliação;
- duração do shutdown.

Usar labels de cardinalidade limitada (`kind`, `status`, `failureCode`, componente); IDs de jogador, carteira, mensagem e transação ficam nos logs, nunca como labels de métricas. Contadores de resultados financeiros avançam após commit; replays têm contador separado. Readiness deve verificar PostgreSQL e SQS com prazos curtos; liveness não consulta dependências externas.

## 17.3. Health checks

- `/health/live`: processo e event loop ativos;
- `/health/ready`: PostgreSQL, SQS e dependências obrigatórias acessíveis.

Falha de readiness não deve, por si só, encerrar o processo.

---

## 18. Estratégia de testes

## 18.1. Testes unitários

### Money

- parsing válido;
- zero;
- duas casas;
- escala excedente;
- negativos externos;
- vazio;
- notação científica;
- `NaN` e `Infinity`;
- limites de `int64`;
- overflow em soma, subtração e negação;
- moedas incompatíveis;
- serialização exata.

### Wallet

- criação válida e inválida;
- crédito e débito;
- saldo insuficiente;
- moeda divergente;
- incremento de versão;
- `LOSS` sem mudança;
- reidratação sem efeito colateral.

### WagerTransaction

- transições válidas;
- transições terminais bloqueadas;
- regras de cada tipo;
- política de zero;
- referências obrigatórias;
- `OPENING` interno;
- rejeição de `OPENING` externo;
- eventos emitidos por resultado.

### Idempotência

- hash estável;
- equivalência HTTP/SQS;
- mesma chave/mesmo conteúdo;
- mesma chave/conteúdo diferente;
- external ID repetido com outra chave.

## 18.2. Testes de integração

Usar PostgreSQL, Keycloak e LocalStack reais em containers.

Cobrir:

- migrations up/down;
- constraints do schema;
- trigger de imutabilidade;
- abertura zero e positiva;
- atomicidade de carteira, ledger e outbox;
- autorização real;
- inbox e redelivery;
- outbox concorrente;
- DLQ;
- workers e lifecycle Fx;
- reconciliação;
- shutdown e liberação de recursos.

## 18.3. Testes obrigatórios de concorrência

1. Enviar a mesma aposta 50 vezes em paralelo e observar um débito.
2. Disputar duas apostas de `80.00` contra saldo `100.00`.
3. Processar carteiras independentes simultaneamente.
4. Repetir cenários com pelo menos três processos e pools separados.
5. Cruzar uma operação recebida por HTTP e SQS.
6. Repetir operações terminalmente rejeitadas.

## 18.4. Testes de recuperação

1. Encerrar consumidor depois do commit e antes do delete SQS.
2. Encerrar publisher depois da publicação e antes de marcar outbox.
3. Abandonar lease de outbox e confirmar retomada.
4. Persistir referência pendente, reiniciar e concluir em outra instância.
5. Entregar `REFUND`/`ROLLBACK` antes da referência.
6. Expirar uma referência nunca recebida.
7. Tornar PostgreSQL ou SQS temporariamente indisponível.
8. Reiniciar todos os processos e repetir os replays.

Pontos de falha controlados deverão ser injetáveis em testes, sem condicionais espalhadas pelo domínio.

### 18.4.1. Evidência exigida e casos adicionais

- Subir três processos do binário com PIDs, portas e pools distintos; goroutines ou três objetos da aplicação no mesmo processo não atendem ao requisito. Usar barreira de início e verificar resultados persistidos.
- Provar paralelismo de carteiras mantendo o lock da carteira A em uma conexão e concluindo operação da carteira B por outra instância, antes de liberar A.
- Para 50 duplicatas, assertar um débito, uma mudança de versão e apenas os eventos financeiros esperados. Em SQS, variar o ID de deduplicação do transporte preservando o envelope para exercitar a inbox; variar também `messageId`, mantendo identidade financeira, para exercitar idempotência. Registrar recebimentos efetivos; FIFO não pode mascarar o teste.
- Injetar interrupção antes do commit, depois do commit/antes do delete, depois do commit/antes do publish e depois do publish/antes da confirmação. Usar sincronização explícita, não sleeps como prova de passagem pelo ponto. Verificar resultado final, inbox, ledger e outbox.
- Concorrer `REFUND` e `ROLLBACK` da mesma `BET`, duas reversões iguais e rollback de crédito sem saldo; validar a cadeia da seção 8.1, rejeições distintas e ausência de ledger nas rejeitadas.
- Testar `WIN` referenciado antes da aposta, autorreferência, incompatibilidade, referência terminal sem sucesso, limite de TTL e evento de pendência emitido uma vez.
- Provar que lease expirado pode ser assumido e que o token antigo não altera resultado/agenda de referência nem confirmação da outbox. Evento confirmado permanece armazenado durante indisponibilidade prolongada do destino.
- Testar saldo histórico de sucesso e rejeição após novas movimentações, conflito de ambas as identidades, hash com chaves reordenadas e `messageId` reutilizado com conteúdo diferente.
- Autenticação: ausência, expiração, issuer/audience/assinatura inválidos; provedor B tenta ler e repetir transação de A por ambos os endpoints; provider tenta todos os endpoints internos. Confirmar ausência de efeitos e de dados expostos.
- Banco: testar FK incompatível, reversão duplicada, `OPENING` duplicado, escrita de saldo sem ledger, alteração de resultado terminal, UPDATE/DELETE/TRUNCATE do ledger e alteração do snapshot de evento usando a role de runtime.
- Reconciliação: histórico vazio, abertura positiva, todas as operações, atualizações concorrentes e divergência induzida com credencial administrativa exclusiva da fixture. Limpar o ambiente de teste sem conceder esses privilégios ao runtime.

Cada cenário deve terminar com saldo igual à soma exata do ledger, versões corretas e contagem de eventos adequada ao tipo. Testes de recuperação que envolvam duplicação externa verificam identidade estável, não exigem que o broker entregue duas cópias quando sua deduplicação ainda estiver ativa.

## 18.5. Verificações finais

```sh
go test ./...
go test -race ./...
go vet ./...
gofmt -w .
```

Testes de integração poderão usar build tag, desde que o comando exato seja documentado.

O harness deve preparar dependências reais, aplicar migrations uma vez, provisionar IdP/filas, aguardar readiness com prazo e isolar IDs/filas por execução. Se forem adotadas tags `integration`, `distributed` e `recovery`, entregar comandos explícitos para cada uma com `-race` nos testes aplicáveis; `go test ./...` sem tags não comprova essas suítes. Capturar logs por instância em falhas. Migrations de reversão devem ser testadas em banco descartável e documentar perda de dados; nunca testar down contra dados de uso normal.

---

## 19. Plano de execução por fases

## Fase 0 — Decisões e critérios de aceite

### Entregas

- registrar interpretações do enunciado;
- registrar a política de reversões da seção 8.1;
- registrar a política da referência opcional de `WIN` da seção 8.1.1;
- definir status HTTP e failure codes;
- definir timeouts, retries e TTLs iniciais;
- criar matriz requisito → teste.

Usar as decisões já estabelecidas nas seções 7–15 e a matriz da seção 24 como ponto de partida; esta fase registra as justificativas no `ARCHITECTURE.md`, sem reabrir escolhas resolvidas salvo incompatibilidade comprovada. Definir também versões fixas, permissões do produtor SQS e estratégia de evidência do broker local.

### Critério de conclusão

Nenhuma regra financeira relevante permanece implícita.

## Fase 1 — Bootstrap do projeto

### Entregas

- `go.mod` e `go.sum`;
- versão Go fixada;
- estrutura inicial de pacotes;
- Dockerfile multi-stage;
- Docker Compose;
- carregamento e validação de configuração;
- módulos Fx básicos;
- logger;
- servidor HTTP mínimo;
- `.env.example`.

### Testes

- construção do binário;
- composição Fx inicia e encerra;
- configuração inválida falha de forma legível.

### Critério de conclusão

Aplicação sobe localmente e encerra com graceful shutdown.

## Fase 2 — Domínio financeiro

### Entregas

- `Money`;
- `Wallet`;
- `WagerTransaction`;
- `WalletLedgerEntry`;
- eventos tipados;
- erros classificáveis;
- criação e reidratação separadas.

### Testes

- suíte unitária completa das invariantes;
- testes de overflow e transições ilegais.

### Critério de conclusão

Todas as regras podem ser exercitadas sem infraestrutura.

## Fase 3 — Banco e migrations

### Entregas

- migrations up/down;
- constraints;
- triggers de imutabilidade;
- pool pgx;
- repositories;
- unidade de trabalho transacional;
- mapeamento de domínio e persistência.

### Testes

- migrations em PostgreSQL real;
- tentativas diretas de violar saldo, unicidade e ledger;
- rollback transacional.

### Critério de conclusão

As invariantes essenciais permanecem protegidas mesmo fora da aplicação.

## Fase 4 — Autenticação e carteiras

### Entregas

- realm e clients Keycloak provisionados;
- middleware OIDC;
- autorização por roles;
- `POST /wallets`;
- `GET /wallets/:walletId`;
- ledger paginado;
- abertura interna zero e positiva.

### Testes

- token ausente, inválido e expirado;
- provider impedido de criar carteira;
- abertura duplicada;
- abertura positiva atômica;
- abertura zero sem ledger/outbox financeiro.

### Critério de conclusão

Carteiras estão protegidas e abertura cumpre integralmente o contrato.

## Fase 5 — Processamento HTTP e idempotência

### Entregas

- DTOs e parsing estrito;
- JSON canônico e hash;
- caso de uso financeiro;
- locking por carteira;
- `POST /wagering/transactions`;
- consultas por ID interno e externo;
- replay e conflitos;
- resultado histórico persistido;
- implementação dos cinco tipos externos.

### Testes

- regras de cada tipo;
- duplicatas HTTP;
- conflito de payload;
- external ID com outra chave;
- isolamento entre providers;
- duas apostas concorrentes de 80 sobre 100.

### Critério de conclusão

O caminho HTTP já oferece integridade, concorrência e idempotência persistente.

## Fase 6 — SQS e inbox

### Entregas

- provisionamento das filas e DLQ;
- consumidor com long polling;
- inbox transacional;
- retry e visibility timeout;
- tratamento de mensagens inválidas;
- shutdown seguro;
- métricas iniciais de consumo.

### Testes

- reentrega;
- queda após commit;
- message ID com hash divergente;
- cruzamento HTTP/SQS;
- redrive para DLQ;
- múltiplos consumidores.

### Critério de conclusão

HTTP e SQS compartilham resultado e garantias financeiras.

## Fase 7 — Referências pendentes

### Entregas

- estado e agenda persistentes;
- worker concorrente;
- backoff e jitter;
- máximo de tentativas/TTL;
- resolução posterior;
- rejeição por expiração;
- eventos de pendência e rejeição.

### Testes

- referência chega depois;
- referência continua pendente;
- referência termina rejeitada;
- referência incompatível;
- expiração;
- restart e retomada em outra instância.

### Critério de conclusão

Nenhuma pendência confirmada depende da memória ou da instância que a recebeu.

## Fase 8 — Transactional outbox

Esta fase implementa o publisher e a recuperação. Schema, eventos concretos e gravação atômica da outbox são entregues nas fases 2–5; não adiar a atomicidade financeira até aqui.

### Entregas

- outbox no mesmo commit do domínio;
- publisher concorrente;
- leases;
- retry/backoff;
- eventos tipados;
- fila de saída;
- recuperação de trabalho abandonado.

### Testes

- dois publishers disputando registros;
- falha antes da publicação;
- falha depois da publicação;
- republicação com mesmo event ID;
- atraso e tentativas observáveis.

### Critério de conclusão

Nenhum evento confirmado é perdido e nenhuma publicação ocorre antes do commit de origem.

## Fase 9 — Reconciliação e observabilidade

### Entregas

- endpoint de reconciliação;
- snapshot consistente;
- logs estruturados completos;
- métricas;
- liveness e readiness finais;
- correlação entre entrada, transação e evento.

### Testes

- reconciliação consistente;
- divergência artificial detectada em ambiente de teste;
- dependência indisponível refletida em readiness;
- ausência de segredos nos logs.

### Critério de conclusão

Falhas e inconsistências podem ser detectadas e investigadas operacionalmente.

## Fase 10 — Cenários distribuídos e hardening

O harness de múltiplos processos começa na fase 5, com as duas apostas concorrentes; cenários SQS e de interrupção são adicionados nas fases 6–8. Esta fase consolida a matriz e corrige falhas restantes, sem deixar a primeira validação distribuída para o fim.

### Entregas

- harness para três instâncias;
- cenários reproduzíveis de falha;
- timeouts revisados;
- queries e índices analisados;
- race detector limpo;
- tratamento de sinais validado.

### Testes

- toda a matriz obrigatória do desafio;
- indisponibilidade temporária de PostgreSQL e SQS;
- concorrência entre carteiras;
- reinício completo;
- verificação final saldo versus ledger.

### Critério de conclusão

Todos os cenários obrigatórios passam de forma reproduzível.

## Fase 11 — Documentação e entrega

### Entregas

- README operacional completo;
- `ARCHITECTURE.md` com decisões e limitações;
- exemplos de autenticação e chamadas;
- migrations documentadas;
- comandos de testes comuns e de integração;
- instruções de múltiplas instâncias e falhas;
- revisão de `.env.example` sem segredos reais.

### Critério de conclusão

Uma pessoa consegue clonar, subir, testar e compreender a solução sem informação externa.

---

## 20. Ordem de prioridade recomendada

1. Bootstrap reproduzível, dinheiro e invariantes do domínio.
2. Schema, transação financeira e gravação atômica da outbox.
3. Autenticação/autorização e abertura de carteiras.
4. Idempotência, operações HTTP e concorrência multiprocesso.
5. Inbox/SQS e recuperação de referência pendente.
6. Publisher da outbox e recuperação de publicação.
7. Reconciliação e consolidação da observabilidade.
8. Consolidação dos testes de falha e documentação de entrega.
9. Diferenciais opcionais.

Testes, logs e documentação acompanham cada fase. Os 70 pontos de integridade, concorrência, idempotência e mensageria orientam a profundidade das evidências; autenticação é eliminatória e acompanha o primeiro endpoint de negócio.

Partidas dobradas, tracing e testes de carga somente deverão ser considerados depois de todos os requisitos obrigatórios estarem demonstrados.

---

## 21. Riscos principais e mitigação

| Risco | Mitigação |
| --- | --- |
| Duplicidade entre HTTP e SQS | Unicidades compartilhadas e mesmo caso de uso |
| Lost update | Lock PostgreSQL por carteira |
| Deadlock | Ordem estável de locks e transações curtas |
| Crédito duplicado em reversão | Lock da carteira compartilhada com a referência e índice único parcial |
| Evento perdido após commit | Transactional outbox |
| Evento duplicado após publish | Event ID estável e entrega at-least-once explícita |
| Mensagem apagada antes do commit | Delete SQS somente após commit |
| Pendência perdida no restart | Estado, tentativas e agenda no PostgreSQL |
| Replay retornar saldo atual | Persistir saldo observado no resultado original |
| Provider acessar outro provider | Provider derivado do token em todo acesso |
| Overflow monetário | Verificação em parsing e aritmética |
| Ledger alterado diretamente | Grants/constraints e triggers de imutabilidade |
| Worker preso durante shutdown | Contexto, prazo, lease e visibility controlados |

---

## 22. Definition of Done geral

A implementação estará concluída quando:

- [ ] todos os endpoints de negócio exigirem autenticação adequada;
- [ ] providers estiverem isolados em escrita, leitura e replay;
- [ ] não existir uso de ponto flutuante para dinheiro;
- [ ] saldo negativo for impossível no domínio e no banco;
- [ ] ledger for append-only e reconciliável;
- [ ] duplicatas HTTP, SQS e cruzadas produzirem um único efeito;
- [ ] resultados históricos forem reproduzidos corretamente;
- [ ] três instâncias processarem concorrentemente sem lost updates;
- [ ] inbox e efeitos compartilharem a mesma transação;
- [ ] outbox e efeitos compartilharem a mesma transação;
- [ ] publicações abandonadas forem retomadas;
- [ ] referências pendentes sobreviverem a reinícios;
- [ ] DLQ e retries estiverem demonstrados;
- [ ] graceful shutdown estiver testado;
- [ ] reconciliação usar uma visão consistente;
- [ ] métricas e logs cobrirem os fluxos críticos;
- [ ] migrations up/down funcionarem;
- [ ] `go test ./...` passar;
- [ ] `go test -race ./...` passar nos testes aplicáveis;
- [ ] `go vet ./...` passar;
- [ ] código estiver formatado com `gofmt`;
- [ ] Docker Compose subir a solução completa;
- [ ] README e ARCHITECTURE permitirem reprodução a partir de checkout limpo;
- [ ] constraints, grants e triggers forem testados com a role real da aplicação;
- [ ] cadeia de reversões, referência opcional de WIN e códigos de erro estiverem documentados e testados;
- [ ] deduplicação da aplicação tiver recebimentos repetidos comprovados;
- [ ] nenhum evento confirmado for descartado ao esgotar tentativas;
- [ ] política do produtor SQS e limites do emulador tiverem evidência explícita;
- [ ] todos os itens da matriz da seção 24 tiverem comando reproduzível e resultado registrado.

---

## 23. Próximo passo imediato

Implementar a Fase 3: provisionar PostgreSQL no Compose, definir migrations up/down com roles e constraints, integrar o pool `pgx`, criar a unidade de trabalho e os mapeamentos dos agregados. A primeira evidência deve executar migrations em PostgreSQL real e provar por SQL direto as restrições de saldo, unicidade e imutabilidade do ledger.

---

## 24. Matriz de rastreabilidade requisito → entrega → evidência

Os números da primeira coluna referem-se às seções do enunciado. Todos os itens estão planejados; marcar concluído somente após executar e registrar a evidência. A matriz cobre também obrigações sem pontuação individual.

| Requisito do teste | Entrega no plano | Evidência mínima de aceite |
| --- | --- | --- |
| §2, §13: autenticação externa e isolamento | §13; fase 4 | IdP real, tokens inválidos/expirados, acesso cruzado e endpoints internos negados sem efeitos |
| §2, §10: credenciais e políticas do broker | §13.3, §15; fase 6 | Provisionamento, produtor permitido e acesso indevido negado; limitações do emulador registradas |
| §4: Go, módulos, Fx, stack e migrations | §5, §16; fases 1–3 | Build reproduzível, go.mod/go.sum, startup/shutdown Fx, migrations up/down em banco descartável |
| §5, §6.1: dinheiro exato e limites | §7.1; fase 2 | Parsing, serialização/persistência sem float, limites int64 e moedas incompatíveis |
| §6: encapsulamento, erros e reidratação | §7; fase 2 | Zero values inválidos, transições ilegais e reidratação sem eventos/movimento |
| §6.2, §9: carteira e OPENING | §7.2, §9, §12.1; fase 4 | Versão 1, abertura positiva atômica; zero sem eventos; conflito de jogador/moeda |
| §5, §6.4: invariantes no banco e ledger append-only | §9; fase 3 | SQL direto inválido rejeitado; role runtime sem edição/exclusão/TRUNCATE; saldo/ledger atômicos |
| §6.3: estados, histórico e retomada | §7.3, §10, §12.4 | Nenhum PENDING intermediário confirmado; pendência retomada; terminal imutável |
| §7: cinco tipos e regras de zero | §8; fases 2 e 5 | BET/WIN positivos; LOSS zero sem ledger/versão e com evento de processamento |
| §7: reversões e referência opcional | §8.1; fases 5 e 7 | Referência e valor validados; disputa REFUND/ROLLBACK, cadeia e débito reverso sem saldo |
| §7: referências fora de ordem | §12.4; fase 7 | Resolução posterior, pendente/terminal sem sucesso, TTL e evento de rejeição após restart |
| §8: coordenação distribuída por carteira | §11, §18.4.1; fases 5 e 10 | Três processos: 100 − 80 = 20, segunda aposta rejeitada, um débito; carteira independente progride |
| §9: contratos HTTP e consultas | §12–14; fases 4–5 | Todos os endpoints, header obrigatório, códigos/corpos distintos, paginação opaca estável |
| §9: hash e duas identidades | §10; fase 5 | Vetores SHA-256, 50 duplicatas, conflito de conteúdo e chave alternativa sem reaplicação |
| §9: replay financeiro histórico | §10, §14; fase 5 | Sucesso/rejeição retornam saldo original após novas operações; autorização no replay |
| §9: reconciliação consistente | §12.6; fase 9 | Snapshot sob escrita concorrente, diferença com sinal correto, checkedEntries e divergência observável sem reparo |
| §6.5, §10: inbox e at-least-once | §12.3, §15; fase 6 | Hash do messageId, cruzamento HTTP/SQS, commit antes de delete e reentrega efetiva |
| §10: retry, DLQ e SIGTERM | §15–16; fase 6 | Mensagem inválida chega à DLQ; falha transitória recupera; trabalho concluído/liberado no prazo |
| §11: outbox concorrente e recuperável | §12.5; fase 8 | Dois publishers, queda nos dois intervalos críticos, lease retomado, eventId estável e nenhuma perda |
| §11: contratos dos quatro eventos | §7.5; fases 2–5 e 8 | Payloads concretos, snapshot imutável, eventos corretos de OPENING/LOSS/rejeição/pendência |
| §12: observabilidade e health | §17; fases 1–9 | Logs sem segredos, métricas exigidas, liveness pública e readiness de PostgreSQL/SQS |
| §13: integração real e race detector | §18; todas as fases | PostgreSQL, IdP e SQS em containers, três processos, reinício total, testes aplicáveis com -race |
| §15: entrega reproduzível | Fase 11 e §22 | Checkout limpo: Compose, IdP/filas automáticos, .env.example, exemplos autenticados e comandos completos |

## 25. Referências técnicas da revisão

Documentação oficial consultada via Context7 para validar ordem consistente de locks e os limites das constraints:

- [PostgreSQL — Explicit Locking](https://www.postgresql.org/docs/current/explicit-locking.html): locks explícitos/implícitos e prevenção de deadlocks pela ordem de aquisição.
- [PostgreSQL — SELECT](https://www.postgresql.org/docs/current/sql-select.html): locking clauses e seleção concorrente de trabalho.
- [PostgreSQL — Constraints](https://www.postgresql.org/docs/current/ddl-constraints.html): FKs compostas e impossibilidade de garantir relações entre tabelas apenas com CHECK.

As políticas de chave alternativa, reversão conservadora, confiança no produtor e prazos de retry são decisões deste projeto, não requisitos adicionais atribuídos à documentação. Ao fixar as versões na fase 1, verificar os detalhes de configuração do Fx, cliente SQL, IdP e SQS nas respectivas documentações; esta revisão não escolhe versões sem validação.
