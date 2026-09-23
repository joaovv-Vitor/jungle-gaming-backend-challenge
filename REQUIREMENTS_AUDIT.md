# Auditoria dos requisitos

Revisão dos requisitos de `teste tecnico.md` em 23/09/2026. **Coberto** significa que há teste automatizado diretamente relacionado; **parcial** significa que falta ao menos um cenário pedido pelo enunciado; **não demonstrado** significa que não há ambiente capaz de provar a propriedade. Esta auditoria não substitui a execução dos comandos abaixo em um checkout limpo.

| Requisito | Estado | Evidência existente e lacuna |
| --- | --- | --- |
| §2, §13: autenticação e isolamento | Coberto | `TestRealKeycloakAuthorizationHasNoUnauthorizedFinancialEffects` usa tokens reais A/B e confere ausência de efeitos em PostgreSQL; `TestKeycloakIssuedTokenIsRejectedAfterExpiry` cria um realm temporário, verifica um token real recém-emitido e confirma sua rejeição após o `exp` assinado pelo IdP. |
| §2, §10: políticas do broker | Coberto | O Compose principal publica somente `sqs-gateway`, que valida SigV4 e aplica `deploy/aws/` antes de encaminhar ao LocalStack isolado. `TestMainSQSRejectsUnauthorizedFinancialMessage` confirma envio autorizado, `AccessDenied` para chave inventada, segredo incorreto, app e identidade de testes, e ausência de novos efeitos financeiros/inbox. `TestIAMPolicyTemplatesUseLeastPrivilegeQueueActions` confere os templates. |
| §4: stack, Fx e migrations | Coberto | Build, bootstrap e Compose verificados; `TestMigrationsUpDownUpInDisposableSchema` aplica todas as migrations, reverte em ordem inversa e reaplica em schema PostgreSQL exclusivo. |
| §5, §6.1: dinheiro | Coberto | `internal/domain/money/money_test.go`: decimal, JSON, moeda, sinal e overflow; persistência `BIGINT` verificada nos testes PostgreSQL. |
| §6: domínio e reidratação | Coberto | Testes de `wallet`, `wagering`, `ledger` e `event` exercitam zero values, transições e reidratação. |
| §6.2, §9: abertura de carteira | Coberto | `TestWalletStoreCreatesOpeningLedgerAndOutboxAtomically` e testes do serviço: saldo positivo, zero, versão e conflito. |
| §5, §6.4: invariantes e ledger | Coberto | A migration 4 valida o histórico e liga `balance_before_minor` ao saldo anterior real. `TestRuntimeRoleRejectsLedgerThatStartsFromInventedBalance` reprova com a role runtime a divergência antes aceita. `TestDatabaseRejectsInvalidFinancialSemantics` e `TestRuntimeRoleCannotMutateLedger` cobrem semântica e imutabilidade. |
| §6.3: estados e retomada | Coberto | Máquina de estados e `TestPendingReferencesResolveAndExpireAfterFullProcessRestart` demonstram pendência persistida, retomada e estados terminais após novo processo. |
| §7: cinco tipos e LOSS | Coberto | `TestWagerServiceProcessesAllKindsAndPendingReference` e testes de domínio cobrem os cinco tipos; LOSS zero não gera movimento. |
| §7: reversões | Coberto | Regras e cadeia sequencial, `TestRefundAndRollbackRaceForSameBet` com duas conexões bloqueadas na mesma carteira e `TestRollbackOfWinRejectsWhenBalanceIsInsufficient`. |
| §7: ordem das referências | Coberto | `TestReferenceWorkerReschedulesAndCompletesAfterReferenceArrives`, rejeição e TTL em PostgreSQL; `TestPendingReferencesResolveAndExpireAfterFullProcessRestart` verifica resolução e expiração, eventos e replay terminal após restart. |
| §8: coordenação distribuída | Coberto | `TestThreeProcessesSerializeAndReplayAfterRestart` sobe três processos, testa disputa, carteira independente e replay após SIGTERM/restart. `TestReadCommittedRetryRollsBackAndRepeatsWholeTransaction` injeta SQLSTATEs `40P01`/`40001` e confirma rollback; `TestReadCommittedRetryRecoversFromRealDeadlock` provoca deadlock real entre duas conexões e confirma a repetição da vítima. |
| §9: HTTP e consultas | Coberto | Endpoints e códigos exercitados em integração; `TestLedgerCursorRemainsStableWhileNewMovementsCommit` verifica cursor vinculado à carteira, limite, ordenação e ausência de repetição/omissão após novos lançamentos. `TestDecodeJSONRejectsBodyBeyondSizeLimit` verifica rejeição do primeiro byte além de 1 MiB, inclusive após um objeto JSON válido. |
| §9: hash e duas identidades | Coberto | `TestCanonicalPayloadVector`, 50 duplicatas e testes de conflitos de identidade em PostgreSQL. |
| §9: replay histórico | Coberto | Replay de sucesso e `TestRefundAndRollbackRaceForSameBet` verificam saldo histórico de rejeição após nova operação; teste com Keycloak nega replay por outro provedor. |
| §9: reconciliação | Coberto | Testes de snapshot concorrente, divergência sinalizada e overflow em `reconciliation_integration_test.go`. |
| §6.5, §10: inbox | Coberto | Testes de commit atômico, cruzamento HTTP/SQS e redelivery em PostgreSQL/LocalStack. |
| §10: retry, DLQ e SIGTERM | Coberto | O consumidor ajusta a visibility de falhas transitórias com backoff de 5 segundos dobrado por recebimento, até 300 segundos; teste unitário verifica as tentativas e a chamada ao broker. Redrive real e recuperação de falhas permanecem cobertos; `TestSIGTERMReleasesInFlightSQSMessageForAnotherProcess` verifica liberação e retomada sem efeito duplo. |
| §11: outbox | Coberto | Testes de publishers concorrentes, lease abandonado, reenvio com `eventId` estável e fila FIFO real. |
| §11: quatro eventos | Coberto | `TestFourEventContractsReachSQSFromFinancialOperations` exercita OPENING, BET, LOSS, rejeição e referência pendente; compara os sete snapshots da outbox às mensagens reais na FIFO, valida payloads e causação e confirma publicação. |
| §12: observabilidade | Coberto | Readiness/liveness, métricas e recuperação são exercitados. O teste HTTP com Keycloak real examina o log do processo; testes dos workers SQS, referência e outbox injetam erros com marcador sensível e verificam categorias seguras. A outbox persiste apenas `publish_<categoria>` em `last_error`. |
| §13: integração e race | Coberto | Suítes com PostgreSQL, Keycloak, LocalStack e três processos existem; executar com `-race` antes de cada entrega. |
| §15: entrega | Coberto | Em 23/09/2026, o Compose principal construiu app e gateway em volume novo; PostgreSQL, Keycloak, LocalStack, gateway e app ficaram saudáveis, migrations `1,2,3,4` e health checks `200` foram confirmados. `gofmt`, `go vet`, `go test -count=1 ./...`, `go test -race -count=1 ./...` e `go test -race -tags=integration -p 1 -count=1 ./...` passaram. A publicação permitida e quatro negações na fila principal foram verificadas contra o banco. |

## Reprodução

Com `docker compose up -d --build` e serviços saudáveis:

```sh
go test -race ./...
go vet ./...
go test -race -tags=integration -run TestRealKeycloakAuthorizationHasNoUnauthorizedFinancialEffects -v ./internal/bootstrap
docker compose stop app
go test -race -tags=integration -p 1 ./internal/adapters/postgres ./internal/adapters/auth ./internal/adapters/sqs ./internal/bootstrap
docker compose start app
```

Para verificar a política no broker do Compose principal, com o app parado para evitar disputa pela fila:

```sh
go test -race -tags=integration -count=1 -run '^TestMainSQSRejectsUnauthorizedFinancialMessage$' -v ./internal/bootstrap
```

A suíte `internal/bootstrap` pode rodar com o app do Compose ativo em geral, mas os cenários da fila principal e de reinício de referência devem rodar com o app parado para que apenas seus processos assumam as mensagens e pendências. Os testes de integração exigem portas publicadas e serviços inicializados.

## Fronteira de confiança do SQS

Provedores externos não publicam diretamente na fila compartilhada: usam o HTTP autenticado. Somente o serviço interno de ingestão recebe `SendMessage` na entrada. O aplicativo recebe `ReceiveMessage`/`DeleteMessage` na entrada, leitura de atributos da DLQ e `SendMessage` na saída. No ambiente local, o gateway assinado e as políticas são aplicados à mesma fila que produz os efeitos financeiros; a porta do LocalStack não é publicada. Em AWS, substitua `REGION`/`ACCOUNT_ID` nos templates e vincule cada política a uma role IAM distinta. IAM identity policy não substitui revisão de queue policy, SCPs ou teste de acesso efetivo na conta alvo. O `providerId` do corpo SQS não autentica a origem.

`TestTransientFailureDefersRedeliveryInSQS` confirmou no LocalStack que uma falha transitória atrasa a reentrega por aproximadamente cinco segundos. `TestMainSQSRejectsUnauthorizedFinancialMessage` confirma no mesmo ambiente que segredo incorreto não autoriza publicação nem movimentação financeira.

Caso se deseje validar IAM em AWS futuramente, execute `TestAWSIAMQueuePermissions` conforme as variáveis `IAM_TEST_*` descritas no README, em conta de teste com filas vazias e sem consumidores. `SKIP`, falha de rede, fila ausente ou simulação de IAM não demonstram acesso efetivo.
