# Retrospectiva da execução do Batuta — Tempo MVP

Cópia literal do documento enviado pelo usuário em 2026-09-21 (produzido na máquina `hermes`, através de um plugin do usuário). Evidências referenciadas ficam naquela máquina. Triagem em inglês em `2026-09-21-tempo-mvp-triage.md`.

---

**Data:** 2026-09-21
**Projeto observado:** `/home/francisross/Projects/tempo`
**Branch:** `feat/weather-mvp`
**Objetivo:** registrar evidências, falhas operacionais e melhorias propostas para uma sessão dedicada ao Batuta.

## 1. Resumo executivo

O Batuta participou de forma real do desenvolvimento do Tempo: estruturou o plano, roteou tarefas, criou worktrees, despachou executores, manteve journal e snapshots, produziu commits e executou um review final. A implementação resultante é funcional e testada.

A execução, porém, não foi autônoma de ponta a ponta. As tarefas 3 e 4 exigiram intervenção frequente do conductor porque o executor não conseguiu executar Bash ou Git, entregas ficaram em `waiting_input` ou `blocked`, worktrees precisaram ser reconciliadas manualmente e o review automático terminou com um resultado contraditório. Bugs funcionais importantes só foram encontrados por uma revisão independente posterior.

A conclusão é:

> O Batuta forneceu estrutura, rastreabilidade e produção de código útil, mas ainda dependeu excessivamente do conductor para recuperar tarefas, executar gates, integrar snapshots e decidir o aceite final.

## 2. Escopo observado

Plano executado: `.batuta/plans/done/tempo-weather-mvp.md`

O plano continha cinco tarefas:
1. modelagem e clientes Open-Meteo;
2. busca de localização e geolocalização;
3. apresentação meteorológica responsiva;
4. integração do fluxo completo;
5. documentação de operação, privacidade e fontes de dados.

Deliveries relevantes: `tempo-weather-mvp-20260920-131750`, `tempo-weather-mvp-20260920-135110`, `tempo-weather-mvp-20260920-140350`.

Review automático: `7328d43dbb679c0e7e009aea295a69ba12bb4013483a00de157740de36380ac3`.

## 3. O que funcionou bem

### 3.1 Planejamento e decomposição
- O plano separou domínio, infraestrutura, apresentação, integração e documentação.
- Cada tarefa possuía escopo de arquivos e critérios de aceite verificáveis.
- As dependências entre tarefas estavam explícitas.
- Os comandos de teste foram definidos antes da implementação.

### 3.2 Roteamento e isolamento
- O roteamento permitiu usar modelos diferentes por complexidade.
- Tarefas `medium+` foram executadas em worktrees isoladas.
- Alterações parciais permaneceram recuperáveis por commits e snapshots.
- O isolamento evitou que um executor modificasse diretamente a branch principal sem reconciliação.

### 3.3 Rastreabilidade
O Batuta preservou: plano original e plano concluído; journal de eventos; estados de delivery; perguntas e respostas vinculadas às tarefas; commits estacionados; snapshots de review; digests de artefatos. Isso permitiu identificar o que ocorreu e recuperar trabalho após interrupções.

### 3.4 Resultado técnico
A execução produziu componentes, clientes Open-Meteo, testes, documentação e integração utilizáveis. Após as correções posteriores, o projeto passou por: 49 testes; ESLint; build Next.js + TypeScript; `git diff --check`; smoke test pela URL Tailscale.

## 4. Problemas encontrados

### 4.1 Falta de qualificação prévia das capacidades do executor
**Observação.** Nas tarefas 3 e 4, o executor Claude informou que não conseguia executar Bash ou Git. Mesmo assim, as tarefas dependiam explicitamente de execução de testes, build, lint, inspeção de diff e criação ou recuperação de commits. A consequência foi a repetição de estados como `waiting_input` e `blocked`.
**Impacto.** O executor produziu código, mas não conseguiu provar o aceite; o conductor precisou executar gates manualmente; tarefas foram estacionadas e retomadas por commits; aumentou o risco de divergência entre o estado do executor e o workspace canônico.
**Melhoria proposta.** Adicionar um preflight de capacidades por rota antes de iniciar a tarefa. O dispatch deve comprovar que o executor consegue cumprir o contrato completo da tarefa, não apenas editar arquivos.
**Critérios de aceite.** O runner verifica ferramentas obrigatórias antes do primeiro prompt; uma rota incompatível falha antes de criar trabalho parcial; o estado diferencia `executor_incapable` de `implementation_failed`; o Batuta pode escalar para uma rota compatível sem duplicar uma submissão incerta; a evidência registra executor, modelo, transporte, permissões e ferramentas disponíveis.

### 4.2 Fronteira pouco clara entre executor, runner e conductor
**Observação.** Quando o executor não conseguiu executar comandos, não havia uma transição simples indicando quem ainda possuía a tarefa, quem deveria executar os gates, se o conductor podia corrigir e integrar imediatamente, e como retomar sem criar uma nova tentativa conflitante.
**Impacto.** O conductor precisou interpretar os estados e assumir manualmente ações de recuperação.
**Melhoria proposta.** Definir uma máquina de estados com uma única autoridade por transição: `executor-owned`, `awaiting-authorized-input`, `conductor-recovery`, `ready-for-gates`, `ready-for-integration`, `accepted | rejected`.
**Critérios de aceite.** Cada estado possui um responsável explícito; a saída do executor inclui arquivos alterados, commit/snapshot, gates executados e pendências; o conductor consegue assumir a recuperação por uma operação declarativa; uma tarefa não pode permanecer simultaneamente sob propriedade do executor e do conductor.

### 4.3 Recuperação e reconciliação excessivamente manuais
**Observação.** Foi necessário recuperar manualmente alterações pelos commits `b5266857125162fb3b7b6ea8b99d07956ff685a4` e `6d58c6aa14ff3c235ae6cdf5485a636f991b74a1`. Também foi necessário remover worktree antiga e executar `cherry-pick` manualmente.
**Impacto.** Maior risco de esquecer alterações; possibilidade de integrar commit errado; dificuldade para saber qual snapshot era canônico; etapas críticas ficaram fora do fluxo automatizado.
**Melhoria proposta.** Criar uma operação de reconcile/resume que: identifica o último snapshot válido; compara-o com a branch canônica; mostra o diff e conflitos; executa gates no ambiente correto; integra somente após validação; registra a proveniência da recuperação.
**Critérios de aceite.** Nenhum `cherry-pick` manual é necessário no caso comum; a operação é idempotente; o Batuta não reaplica commits já integrados; conflitos mantêm o workspace intacto e produzem instruções acionáveis.

### 4.4 Worktrees e artefatos internos contaminaram os gates
**Observação.** Vitest e ESLint passaram a analisar arquivos gerados em `.batuta/**`, incluindo worktrees, snapshots e artefatos de review. Foi necessário alterar manualmente `vitest.config.ts` e `eslint.config.mjs` para excluir `.batuta/**`.
**Impacto.** Gates falharam por artefatos do orquestrador, não pelo produto; o estado da delivery ficou mais difícil de interpretar; uma worktree antiga interferiu na validação do repositório principal.
**Melhoria proposta.** O Batuta deve impedir por padrão que seu diretório operacional seja descoberto por ferramentas do projeto.
**Critérios de aceite.** O bootstrap detecta test runners, linters, typecheckers e formatadores conhecidos; o doctor alerta quando `.batuta/**` entra no escopo dos gates; worktrees são criadas fora de caminhos normalmente varridos, quando possível; a limpeza final verifica worktrees órfãs e listeners/processos associados.

### 4.5 Perguntas e autorizações repetidas durante a mesma tarefa
**Observação.** A tarefa 4 voltou para `waiting_input` mais de uma vez, mesmo após autorização limitada para modificar os arquivos necessários ao estado "nenhum resultado encontrado".
**Impacto.** Interrupções desnecessárias; perda de continuidade do executor; custo adicional de contexto; maior probabilidade de uma tentativa terminar estacionada.
**Melhoria proposta.** Permitir grants de escopo explícitos e persistentes durante uma tentativa: arquivos permitidos, operações permitidas, gates permitidos, validade da autorização.
**Critérios de aceite.** Uma autorização vinculada ao `task_id` e `execution` não é solicitada novamente sem expansão real de escopo; novas perguntas explicam exatamente a diferença em relação ao grant anterior; reinícios não transformam silenciosamente uma autorização limitada em autorização ampla.

### 4.6 Review automático contraditório e pouco acionável
**Evidência.** O review automático registrou `outcome: incomplete_coverage`, `acceptance: pending`, `exit_code: 3`. O relatório, porém, declarou todos os critérios como `satisfied`, mostrou `Findings: None` e finalizou com `Verdict: REWORK`. O arquivo estruturado continha apenas `null`.
**Impacto.** Não havia finding concreto para corrigir; o conductor não conseguia distinguir falha de cobertura, falha da ferramenta ou defeito do produto; o resultado exigiu interpretação manual e nova revisão independente.
**Melhoria proposta.** Tornar o contrato do review internamente consistente e validado antes da publicação.
**Critérios de aceite.** `REWORK` exige pelo menos um finding estruturado ou uma falha operacional estruturada; `findings.json` deve ser sempre uma lista válida, mesmo quando vazia; se todos os critérios estão satisfeitos, a ferramenta não pode retornar `incomplete_coverage` sem explicar a lacuna exata; falha do review deve ser separada de falha do produto; o manifest deve indicar quais comandos foram realmente executados e quais foram apenas herdados como evidência.

### 4.7 Métrica de cobertura do review não representou o aceite do plano
**Observação.** O relatório mostrou `Coverage: 0/1 cohorts`, `Cohort 1: ... uncovered: exit 1`, `Lint: not configured`. Ao mesmo tempo, listou evidências de `pnpm lint` e marcou todos os critérios como satisfeitos.
**Melhoria proposta.** Separar: cobertura do diff; cobertura dos critérios do plano; execução dos gates; disponibilidade da configuração de lint; falhas internas do reviewer. Essas dimensões não devem ser reduzidas a um único resultado ambíguo.

### 4.8 Gates verdes não detectaram bugs de concorrência e acessibilidade
**Observação.** Uma revisão independente encontrou três bloqueadores não identificados pelo review do Batuta: negar geolocalização após uma previsão pronta podia deixar a interface em loading; um callback antigo de geolocalização podia sobrescrever uma cidade escolhida manualmente; a opção ativa do combobox tinha ARIA, mas não possuía destaque visual.
**Impacto.** O plano e os testes existentes davam uma confiança maior do que a implementação realmente merecia.
**Melhoria proposta.** Adicionar uma etapa adversarial de review orientada a riscos, distinta da simples reprodução dos critérios existentes.
**Cenários mínimos sugeridos.** Callbacks assíncronos chegando fora de ordem; retry após sucesso anterior; permissão concedida, negada e alterada durante a sessão; duas ações concorrentes para a mesma fonte de estado; teclado, foco, `aria-selected` e indicador visual; desmontagem de componentes com request pendente; comportamento com API ausente ou parcialmente suportada.
**Critérios de aceite.** O reviewer deve propor cenários novos, não apenas executar os testes declarados no plano; findings de concorrência e acessibilidade devem possuir reprodução determinística; a aprovação final deve distinguir "gates passaram" de "review adversarial aprovado".

### 4.9 A validação do ambiente final ficou fora do aceite efetivo
**Observação.** A aplicação funcionava em `localhost`, mas a primeira exposição por Tailscale entregou HTML e CSS sem hidratação do React. O Next.js em desenvolvimento bloqueou recursos acessados pelo domínio `hermes.lynx-kelvin.ts.net`. Um simples `HTTP 200` não detectou o problema.
**Limite de responsabilidade.** Esse problema ocorreu após o loop principal e não deve ser atribuído integralmente ao Batuta. Entretanto, revela uma lacuna quando o plano inclui execução, preview, staging ou entrega remota.
**Melhoria proposta.** Permitir critérios de aceite vinculados ao ambiente final: URL final, origem final, modo dev ou produção, hidratação/interatividade, smoke tests de ações críticas, logs sem bloqueios relevantes.
**Critérios de aceite.** HTTP 200 isolado não conta como smoke test interativo; o fluxo principal deve ser exercitado pela mesma origem entregue ao usuário; o relatório deve registrar URL, modo do servidor e ações verificadas; recursos JavaScript, chamadas de API e eventos de interface devem ser validados.

## 5. Melhorias priorizadas

**P0 — Confiabilidade e verdade operacional.** Validar capacidades do executor antes do dispatch; corrigir o contrato do review (resultado, findings e cobertura consistentes); separar falha operacional do reviewer de defeito do produto; criar handoff explícito de propriedade entre executor e conductor.

**P1 — Recuperação e automação.** Implementar reconcile/resume para snapshots e commits estacionados; tornar grants de escopo persistentes por tentativa; impedir que `.batuta/**` contamine gates; limpar e verificar worktrees órfãs automaticamente; executar review adversarial para concorrência, estados e acessibilidade.

**P2 — Qualidade da entrega.** Adicionar smoke tests pela origem final entregue ao usuário; registrar ambiente, URL e modo do servidor no aceite; produzir um resumo de proveniência por tarefa; exibir métricas de intervenção manual e retries.

## 6. Métricas sugeridas para futuras execuções

Registrar por delivery: tarefas concluídas sem intervenção; tarefas que entraram em `waiting_input`; tentativas por tarefa; trocas de executor/modelo; tempo do executor versus tempo de recuperação; número de comandos executados manualmente pelo conductor; commits/snapshots recuperados manualmente; worktrees órfãs encontradas; findings do review automático; findings encontrados apenas por revisão independente; gates repetidos por contaminação do ambiente; custo e latência por rota.

`autonomia = tarefas aceitas sem intervenção manual / total de tarefas`, acompanhada de qualidade: aumentar autonomia sem preservar o aceite não representa melhoria.

## 7. Agenda sugerida para a sessão de melhoria

1. Reproduzir uma tarefa cujo executor não possui Bash/Git.
2. Mapear a máquina de estados atual de dispatch, ask, retry, block e resume.
3. Definir o contrato de handoff executor → conductor.
4. Reproduzir o review contraditório usando os artefatos desta delivery.
5. Validar o schema de `findings.json` e as invariantes de resultado.
6. Projetar reconcile/resume com idempotência.
7. Definir isolamento padrão para `.batuta/**` e worktrees.
8. Acrescentar review adversarial e smoke test da origem final.
9. Escolher uma pequena correção P0 para implementar e testar de ponta a ponta.

## 8. Perguntas para orientar a investigação

- O runner conhece antecipadamente as ferramentas disponíveis no executor?
- Quem possui autoridade para executar gates quando o executor não consegue fazê-lo?
- Um `waiting_input` preserva de forma inequívoca o snapshot canônico?
- Como o Batuta distingue submissão não iniciada, parcialmente executada e concluída sem gates?
- Por que `findings.json` pôde ser `null`?
- Quais invariantes deveriam impedir `Findings: None` junto de `Verdict: REWORK`?
- Por que o review informou `Lint: not configured` quando `pnpm lint` existia?
- O cálculo de cohort mede cobertura do diff ou cobertura dos critérios?
- Como garantir que retries do host não criem tentativas extras fora da política do Batuta?
- Como medir a quantidade real de intervenção do conductor?

## 9. Evidências locais (máquina hermes)

Plano: `/home/francisross/Projects/tempo/.batuta/plans/done/tempo-weather-mvp.md`.
Runs: `/home/francisross/Projects/tempo/.batuta/runs/supervision/tempo-weather-mvp-20260920-{131750,135110,140350}.json`.
Review: `/home/francisross/Projects/tempo/.batuta/reviews/supervision/7328d43dbb679c0e7e009aea295a69ba12bb4013483a00de157740de36380ac3/artifacts/{review.md,findings.json,manifest.json,state.json}`.
Commits: `82f0e29` loop blocked; `e550816` task_3 e2 parked; `def84a0` fix: complete weather presentation gates; `8096bc9` task_4 e3 parked; `75a8f9f` fix: complete weather application flow; `d57ddf8` loop abandoned; `540b74e` loop done; `cc4bae7` fix: isolate generated Batuta artifacts from quality gates; `0818bd7` fix: prevent stale location updates; `ee55dd1` fix: allow Tailscale development origin.

## 10. Definição de sucesso para a próxima versão

Uma nova execução equivalente deve ser considerada melhor quando: o executor é qualificado antes do dispatch; nenhuma tarefa fica bloqueada apenas porque a rota não possui ferramentas necessárias; recuperações comuns não exigem `cherry-pick` manual; `.batuta/**` não interfere nos gates; o review nunca produz resultados contraditórios; bugs assíncronos e de acessibilidade são exercitados por review adversarial; a origem final é validada de forma interativa; o relatório final informa claramente o que foi feito pelo executor, pelo Batuta e pelo conductor; a quantidade de intervenção manual é mensurável e menor que nesta delivery.
