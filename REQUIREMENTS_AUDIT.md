# Auditoria dos requisitos

Revisão da matriz da seção 24 de `IMPLEMENTATION_PLAN.md` em 23/09/2026. **Coberto** significa que há teste automatizado diretamente relacionado; **parcial** significa que falta ao menos um cenário pedido pela matriz; **não demonstrado** significa que não há ambiente capaz de provar a propriedade. A comprovação de políticas IAM efetivas fica fora do escopo de execução local deste teste técnico; essa limitação não impede o encerramento da Fase 10. Esta auditoria não substitui a execução dos comandos abaixo em um checkout limpo.

| Requisito | Estado | Evidência existente e lacuna |
| --- | --- | --- |
| §2, §13: autenticação e isolamento | Coberto | `TestRealKeycloakAuthorizationHasNoUnauthorizedFinancialEffects` usa tokens reais A/B e confere ausência de efeitos em PostgreSQL; `TestKeycloakIssuedTokenIsRejectedAfterExpiry` cria um realm temporário, verifica um token real recém-emitido e confirma sua rejeição após o `exp` assinado pelo IdP. |
| §2, §10: políticas do broker | Não demonstrado localmente | O adaptador aceita a cadeia padrão de credenciais AWS, sem exigir chaves estáticas ou endpoint local. `TestIAMPolicyTemplatesUseLeastPrivilegeQueueActions` verifica localmente as ações e recursos exatos dos templates em `deploy/aws/`, mas não prova enforcement. `TestAWSIAMQueuePermissions` pode verificar três perfis IAM e filas FIFO isoladas; sem `IAM_TEST_*`, registra `SKIP`. O LocalStack Community aceitou `GetQueueUrl` com credenciais fictícias e não prova negação. O enunciado exige a integração SQS local e o controle por políticas do broker, mas não exige uma conta AWS real; a execução em AWS é opcional e fora do escopo deste teste técnico. |
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
| §15: entrega | Coberto | Um ensaio anterior em checkout Git temporário limpo confirmou build, readiness, migrations `1,2,3`, filas e autenticação real. Após a migration 4, outro projeto Compose construído da árvore de trabalho atual iniciou com volume novo, readiness `200` e migrations `1,2,3,4`; stack e volume foram removidos. A matriz completa de integração com `-race`, `go test -race ./...` e `go vet ./...` passaram após as mudanças. |

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

A suíte `internal/bootstrap` pode rodar com o app do Compose ativo em geral, mas o cenário de reinício de referência deve rodar com o app parado para que apenas seus processos assumam as pendências. Os testes de integração exigem portas publicadas e serviços inicializados. Uma execução bem-sucedida não fecha as lacunas descritas na tabela.

## Fronteira de confiança do SQS

Provedores externos não publicam diretamente na fila compartilhada: usam o HTTP autenticado. Somente um serviço interno de ingestão confiável recebe permissão de `SendMessage` na entrada. O aplicativo recebe `ReceiveMessage`/`DeleteMessage` na entrada, leitura de atributos da DLQ e `SendMessage` na saída. Substitua `REGION`/`ACCOUNT_ID` nos templates e vincule cada política a uma role IAM distinta. IAM identity policy não substitui revisão de queue policy, SCPs ou teste de acesso efetivo no ambiente alvo. O `providerId` do corpo SQS não autentica a origem.

`TestTransientFailureDefersRedeliveryInSQS` confirmou no LocalStack que uma falha transitória atrasa a reentrega por aproximadamente cinco segundos. O teste técnico não pede conta AWS real. A integração SQS local e os modelos de políticas estão presentes, mas o LocalStack Community não comprova negação de acesso por identidade do broker; esse ponto permanece uma limitação documentada.

No LocalStack Community atual, um `awslocal sqs get-queue-url --queue-name wager-transactions.fifo` com `AWS_ACCESS_KEY_ID=untrusted-test` e `AWS_SECRET_ACCESS_KEY=untrusted-test` retornou a URL. O teste local dos templates impede ampliações acidentais das permissões declaradas, mas este ambiente não atende à evidência de acesso indevido negado. Não use os templates para alegar enforcement local. A [documentação de configuração do LocalStack](https://github.com/localstack/localstack-docs/blob/main/src/content/docs/aws/customization/configuration-options.md) identifica o enforcement de IAM como recurso Pro.

Caso se deseje validar IAM em AWS futuramente, execute `TestAWSIAMQueuePermissions` conforme as variáveis `IAM_TEST_*` descritas no README, em conta de teste com filas vazias e sem consumidores. `SKIP`, falha de rede, fila ausente ou simulação de IAM não demonstram acesso efetivo.
