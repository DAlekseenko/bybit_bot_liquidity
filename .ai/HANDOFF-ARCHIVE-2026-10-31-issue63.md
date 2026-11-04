# Передача работы

## Checkpoint — Issue #63 CM-008 v1 aggregate, ГОТОВЫЙ КОНТРАКТ, до developer

- 2026-10-31: final system-designer /root/design63 (Sol6.1/high) завершил только
  остаток агрегатора; [Issue63](https://github.com/DAlekseenko/bybit_bot_liquidity/issues/63)
  обновлена до `CM-008 v1 aggregate`, полный body readback EXACT PASS.
  Durable copy: /private/tmp/issue63-gates/after206/CM-008-final-contract.md.
  Wording уточнён: fee0 допустим, negative/invalid fee отвергает child.
- Same branch codex/63-baseline-targets, worktree
  /Users/alekseenkodenis/.codex/worktrees/647f/bybit_bot_liquidity.
  HEAD=refreshed origin/master=a760d1cba1116587a11df9092639b18ea5823af8.
  PR207 merged_at2026-10-30T21:02:22Z, merge этот SHA, ancestry exit0;
  #206 live closed/completed. #204/#206 APIs доступны, их не менять.
- D-01 окончательно утверждён comment6532663082 и merged SPEC/DECISIONS.
  Повторно не спрашивать. Pure planning, без trade/deploy/risk authority.
- ГОТОВЫЙ API: BaselineTargets(snapshot model.PortfolioSnapshot,
  active map[string]bool, qtySteps map[string]model.Decimal,
  feeRate model.Decimal) ([]model.Target,error).
  Typed BaselinePlanReason: ReasonInvalidBaselineInput/ReasonNonPositiveEquity.
  Exact3 BTCUSDT/ETHUSDT/HYPEUSDT keys в Prices/Positions/active/steps; explicit flat0.
  Exact equity/3, sorted targets; BaselineQuantity для всех3 включая inactive,
  затем inactive Zero. Child/Decimal errors unchanged, любая ошибка strictly nil.
  Nonpositive equity отвергается. Никакого перераспределения budgets/dust.
- Полная matrix сохранена в Issue: 8masks, разные P/Q0/steps, input/result
  immutability, reinvest/costs, NewTradePlan integration, missing/extra keysets,
  nonpositive equity/zero snapshot, typed late child errors/infeasible/overflow
  active+inactive, nil/no partial. Scalar math/oracle не дублировать.
- Whitelist РОВНО6: planner/baseline.go, baseline_test.go, SPEC.md,
  .ai/HANDOFF.md, .ai/HANDOFF-ARCHIVE-2026-10-30-issue63.md (existing byte-exact),
  .ai/HANDOFF-ARCHIVE-2026-10-31-issue63.md (NEW byte-exact этого pre-edit HANDOFF).
  <=250production и <=150 ВСЕХ readable physical testlines после gofmt;
  allocation30+36+24+42+18. Actual exceed → designer, не минификация/снятие кейсов.
  DECISIONS/scalar/model/dependencies/другиеIssues не менять.
- Лимит последняя проверка primary98%, secondary88%, ordinaryAllowed=true.
  Developer/gates/reviewer/publish не запускались: остатка для полного цикла нет.
  Go-code/commit/push/PR #63 отсутствуют; reviewer GREEN/CI #63 отсутствуют.
- ТОЧНЫЙ NEXT STEP после восстановления лимита: перечитать live Issue63 v1,
  fresh status/base/PR duplicate check, сохранить dirty records; архив31 создать
  побайтно из последнего pre-edit checkpoint; активировать ОДНОГО developer-senior
  Sol6.1/medium (exact equity/reserve/current positions/immutable models).
  Реализовать ГОТОВЫЙ контракт без повторного design, кроме реального перерасхода.
  gofmt,targeted/full/race,vet,build,lint,govulncheck,coverage,diff. Final HANDOFF
  до fresh independent reviewer/high final blobs; freeze после GREEN, тот же
  owner ordinary commit/push/onePR; coordinator exact head/base/files/CI.
  Parent merge/closure/archive; tools messages parent не отправлять.
- Dirty prior checkpoint/архив и base206 HANDOFF побайтно сохранены в
  /private/tmp/issue63-gates/after206/. Manifest там же; git diff --check PASS.
  Archive30 SHA256 e8ad3339a711820e10a12d4a8e040786f6eb528a7fa0be0c684c00c0fc9060f6.
  Archive31 пока НЕ создан: developer копирует актуальный checkpoint до edits.
- Signalcollector NOT_READY (runtime/persistence/restart/runbook нет).
  Known module-only Windows GO-2026-5024 не перепроверялся, не all-clear.
  No realAPI/credentials/orders/VPS/deploy/risk-limit changes.
