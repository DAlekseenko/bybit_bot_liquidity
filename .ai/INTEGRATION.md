# Integration queue protocol

Нормативный протокол параллельной разработки и единственного финального slot.
Он дополняет AGENTS.md; Issue body остаётся контрактом, GitHub остаётся
доказательством commits/PR/CI/merge, а очередь не выдаёт новых полномочий.

## Canonical queue и coordinator

Единственная очередь — один top-level comment Epic #55:
https://github.com/DAlekseenko/bybit_bot_liquidity/issues/55#issuecomment-6668604794
с marker `<!-- codex-integration-queue:v1 -->`. Начальный coordinator — root chat
`01a1a83c-8caa-7e91-96c6-a7eee06d19e2`; это bootstrap identity, не указание
текущего mutable owner. Единственный writer — текущая coordinator identity из header comment. Там же хранятся owner, монотонная integer
revision, slot (`none` или точный Issue) и transfer record. Не создавать вторую
очередь, индекс или mutable registry в Git.

Перед каждым изменением coordinator перечитывает exact comment и сверяет URL,
identity, revision, всё тело и актуальные GitHub evidence. Записывает следующую
revision и проверяет URL, owner, revision и полное тело readback. До успешного
readback mutation не подтверждена. Это процедурный single-writer, не CAS,
распределённый mutex или атомарная блокировка. Дубли marker, unknown URL/owner,
stale/mismatch state или unknown outcome блокируют final mutations: остановиться,
reconcile comment и GitHub, не затирать, не угадывать revision и не retry вслепую.
Timeout не освобождает slot и не подтверждает согласие.

Transfer возможен только явно назначенному successor в том же comment с from/to,
новой revision и readback; slot должен быть `none`, незавершённых final actions нет.
Старый owner прекращает записи; successor сначала проверяет transfer. Недоступность
owner не разрешает takeover: нужны explicit owner recovery decision, reconciliation
и readback.

## Records, выбор, shared paths

Queue rows содержат Issue URL/contract version, developer owner, task/chat,
worktree/branch, PR URL, dependency, base/head, approved paths и final hashes/GREEN,
checks, phase/blocker/next action, exact shared reservations. Неизвестное явно
`unknown`, никогда не READY/GREEN/merge proof.

Фазы: `DEV`, `READY_FOR_INTEGRATION`, `ACTIVE_SINGLE_SLOT`, `WAIT`, `WAIT_OWNER`,
`DONE`. Параллельный DEV разрешён только с отдельными per-Issue chat, branch,
worktree и независимым Git index; общий mutable index запрещён, как и совместный
staging/commit из одного worktree. Помимо этой изоляции approved paths/contracts должны
быть непересекающимися. READY означает завершённую disjoint разработку и выполненные dependencies/owner
решения, но не final GREEN/CI на последней базе. Coordinator выбирает READY по
approved priority/dependencies, затем oldest Issue; WAIT_OWNER вне slot не блокирует
независимые READY. Phase и priority не дают полномочий.

До первого edit shared path coordinator резервирует точный whole path за одной
Issue и developer owner и подтверждает readback. Минимальный каталог: `docs/crypto-momentum/SPEC.md`,
`docs/API.md`, `internal/momentum/storage/migrations.go` (может отсутствовать),
плюс любые фактические overlap paths, включая `go.mod`/`go.sum`. Никаких wildcard,
section ownership или одновременных optimistic edits. Ожидающий может работать в
своих disjoint paths; shared edit ждёт release и свежей базы. Reservation от первого
admission до completion/release, не расширяет контракт и не переносит чужие edits.
При неизвестном overlap остановить этот path, сохранить drafts и reconcile. Только
catalog owner на refreshed master назначает migration versions; duplicate version
или changed payload fail-closed, applied migrations immutable. Этот workflow не меняет
SQL, versions, catalog или schema.

## Единственный integration slot

Bootstrap developer сообщает READY в commentary и `/private/tmp/issue<n>-ready.json`
с фактическим номером Issue. Это notification, не queue. Root перечитывает canonical
queue, переводит row в ACTIVE, читает обратно и выдаёт явный slot grant. До grant
разрешены disjoint implementation, reads/fetch и предварительные проверки; запрещены
final base merge, final review и publication. Нельзя параллельно сливать latest default
base в Issue branches. Подтверждённая база при создании branch не захватывает slot.

В slot тот же owner обычным merge обновляет свою ветку от refreshed default branch
(или фиксирует доказанный no-op, если она актуальна), разрешает только approved-scope
conflicts, выполняет checks, обновляет handoff и получает fresh independent GREEN
exact paths/hashes. Лишние conflict paths требуют contract adjustment и budget recheck.
Затем owner обычным commit/push публикует лишь approved blobs, создаёт один PR в
default branch, проверяет URL/head SHA и останавливается. PR/body: `Refs #N`, без
Closes/Fixes/Resolves. Reviewer read-only, independent; не редактирует и не публикует.

Coordinator сверяет contract/base/head/files/GREEN и все configured CI на exact head.
Лишь coordinator выполняет разрешённые merge, ancestry proof, marker checkpoint
readback, closure readback и точный implementation-thread archive readback согласно
AGENTS.md/#221. Slot/shared reservations удерживаются до всей цепочки. `DONE`/release
только после `archived=true`. Failure сохраняет ACTIVE/blocker/next action; resume
использует те же artifacts без timeout release и дублей. Независимый DEV может идти.

Единственная suspension ACTIVE→WAIT_OWNER со slot=none допустима до merge лишь при
известном существенном owner blocker. До записи developer подтверждает STOP: нет
выполняющихся или запланированных commit/push/publication. Coordinator сверяет
quiescence, отсутствие in-flight merge mutation и armed auto-merge/merge queue, exact
Issue/PR/head/base и полный GitHub/Git snapshot. Любое unknown запрещает suspension.
Одно queue update/readback фиксирует причину, STOP evidence, owner/task/worktree/branch,
GREEN/CI references, все reservations и точный released list; blobs/artifacts остаются.
Issue не DONE и не возобновляется автоматически. После ответа owner coordinator
повторно проверяет контракт, dependencies и owner decisions. Тот же task получает
shared reservations заново; до readback reservations shared edits запрещены. Затем
READY, новый grant, refreshed base/relevant diff, новый GREEN и exact-head CI.
Unknown merge outcome, in-flight mutation и pending post-merge/archive failure остаются
ACTIVE. Suspension никогда не маскирует эти blockers.

Если base или blobs изменились, прежние GREEN/CI недействительны: refreshed base/diff,
обычный merge при необходимости, новые проверки, independent GREEN и exact-head CI.
Запрещены force/rebase/amend, silent override и исключение handoff/conflict edits.
Frozen handoff не переписывают ради queue facts. После внешнего шага очередь обновляется
с evidence, но не заменяет его; сбой между шагом и записью требует read-only reconcile.
Не выводить DONE из отсутствующего PR или совпавшего head.

## Bootstrap и acceptance

Root единолично находит/переиспользует marker, seed row с точными identities,
reservations и неизвестными фактами, читает обратно; не создаёт unrelated rows.
Уже данная owner authorization для conditional merge может быть передана coordinator
в конкретный task с исходной границей; повторное approval не требуется без существенной
новой причины или непокрытого действия. Implementation chat не становится coordinator.
Completion/deployment, risk и live-trading authority остаются ограничены AGENTS.md.

Acceptance audit: A1 disjoint DEV одновременно в отдельных Issue chat/branch/worktree
и независимых Git indexes, без общего mutable index или смешанного staging/commit; A2 shared whole-path один owner и
ожидающий fresh-base; A3 priority/dependency/oldest выбирает одну ACTIVE и пропускает
внешний WAIT_OWNER; A4 stale/unknown/duplicate/timeout fail-closed, transfer explicit
и quiescent; A5 base/blob movement требует новый GREEN/CI; A6 seed unknown честен,
readback точен, недоступность GitHub не равна empty/pass; A7 resume сохраняет artifacts,
ambiguous merge reconciles, archive failure удерживает ACTIVE; A8 transfer только при
slot none и readback, без заявления атомарности; A9 #221 history/freeze/archive и legacy
пути сохранены, operational evidence вне frozen blobs; A10 known substantive owner blocker
разрешает только подтверждённый quiescent ACTIVE→WAIT_OWNER/slot none с exact snapshot,
full reservations/released list/readback; после owner response нужны renewed reservations,
READY, new grant и fresh base/GREEN/CI; unknown или post-merge/archive failure остаётся ACTIVE.

Для этой docs/profile Issue: TOML parse, metadata/bytes invariance profiles вне
`developer_instructions`, whitelist и полный diff budget, links, `git diff --check`,
A1-A10 audit, legacy/#221 policy unchanged. Go/race/vet/build/lint/security/coverage
локально пропустить с docs-only причиной; configured exact-head CI остаётся обязательным.
