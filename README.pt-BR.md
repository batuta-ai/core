# Batuta Core

> *Quem rege não toca.*

O núcleo Go do Batuta reúne as partes determinísticas do ciclo de condução:
roteamento, entregas, worktrees, execução, verificações, diário e integração.
Os hosts usam o binário deste módulo em vez de reimplementar essas regras.

## Uso do binário

```text
batuta loop --dry-run [<plano>]        mostra ondas e executores sem executar
batuta dispatch --brief-file <arquivo> --executor <id> --model <id> --cwd <worktree>
                                       executa uma tentativa externa limitada
batuta loop [<plano>]                  executa um plano aprovado
batuta loop --resume <entrega>         continua uma entrega interrompida
batuta loop --answer <tarefa> "<texto>" responde uma tarefa aguardando entrada
batuta loop --supervise <entrega> --cursor <caminho-absoluto>
                                       observa localmente em primeiro plano, sem consultar modelo
batuta loop --dashboard [<entrega>]    imprime um retrato TSV da entrega
batuta review --base <ref> [--spec <plano>] revisa uma entrega pelos adaptadores
batuta watch [<entrega>]               abre o painel interativo ao vivo
batuta trail [<entrega>]               mostra os registros do diário
```

`dispatch` e `loop` aceitam `--transport cli|acp|auto`; sem a opção, o caminho
CLI legado continua sendo o padrão. ACP exige qualificação exata por executor,
versão, plataforma, modelo e esforço. O construtor no código-fonte qualifica
apenas OpenCode 1.18.31 (`opencode acp`), modelo `opencode/big-pickle`, sem
esforço, em macOS arm64 nativo; Linux e Windows continuam via CLI. Isso não
descreve as capacidades de um binário beta23 instalado. Uma tentativa ACP
incerta é preservada para reconciliação e nunca é repetida automaticamente via
CLI. Subagentes nativos
pertencem ao host interativo, não ao binário. Consulte
[dispatch](docs/dispatch.md) e o
[protocolo de medição](docs/dispatch-measurement.md).

A [supervisão do loop](docs/loop-supervision.md) vem habilitada nas execuções
normais de plano, retomada, resposta e roadmap, com cursor durável em
`.batuta/runs/supervision/<entrega>.json`. Logs de execução continuam no stdout;
JSON do observador vai para stderr. Dry-run, dashboard e abandon não iniciam
revisores. `--supervise` continua disponível como acompanhamento separado, com
JSON no stdout. O host precisa manter o processo em execução; notificações
por arquivo local ou desktop compatível são opt-in, e sem um destino os eventos
permanecem não lidos. A observação não faz chamadas a modelos. Após a conclusão,
o supervisor executa uma revisão completa da entrega imutável mesmo sem
`--policy`. Revisão pendente retorna `review_blocked` (código `2`); erros de
execução ou evidência retornam `1`. Revisão ausente, falha, incompleta ou incerta
bloqueia duravelmente as próximas fases. `SHIP` completo libera apenas a
progressão. Retome com `batuta loop --resume <entrega>` ou execute `--roadmap`
novamente; os orçamentos e tentativas incertas são preservados. Consulte sem
executar com `batuta loop --supervise <entrega> --review-status`. O julgamento
explícito usa `--review-judgment accept|reject --review-id <id>
--review-digest <sha256:digest> --rationale "<motivo>"` para a mesma entrega;
veja o guia de supervisão para os comandos e limites de recuperação. Uma política
pode fornecer a resposta fixa e limitada ao escopo da tarefa e retomá-la com as
configurações normais de execução, ou reservar uma proposta de correção
explicitamente autorizada. A conclusão após a retomada inicia a mesma revisão
automática; propostas de correção nunca iniciam um executor. Conclusão da implementação,
resultado da revisão e aceitação pelo condutor são estados distintos. `job.json`,
a cópia imutável da especificação e o snapshot do código ficam em
`.batuta/reviews/supervision/<job-id>/`; somente a saída do motor fica no
subdiretório `artifacts/`. A CLI de supervisão não oferece uma opção de timeout
da revisão, portanto usa o padrão fixo de uma hora. Cancelamento ou timeout
enquanto aguarda a aquisição da posse retorna um erro sem transição do job. Uma
interrupção da sondagem ou do snapshot resulta em `failed`/`execution_failed`. Cancelamento ou
timeout enquanto o motor está em execução resulta em
`uncertain`/`cleanup_unresolved`, sem repetição automática; a verificação após a
saída do motor pode resultar em `failed`/`execution_failed`. A recuperação de um
estado durável `launching` resulta em `uncertain`, pode deixar o resultado sem
valor e não repete a execução automaticamente.

Quando um limite de uso dura além do orçamento de espera, o loop recorre ao
próximo runtime executável sem gastar uma nova tentativa nem uma escalação.

O painel mostra contexto, progresso, tarefas, verificações, commits e a saída
do executor. Ele mostra a presença do loop, abre um editor de resposta com
várias linhas usando `r`, retoma uma resposta enviada em segundo plano, abre o
seletor de entregas com `d` e nunca sai sozinho. Use `?` para ver a legenda
completa de teclas e presença, `--once` para imprimir um único quadro ou
`--interval` para definir a frequência de consulta ao diário. Sem um TTY, a
entrada pelo teclado fica desativada e a task ativa é seguida automaticamente;
`NO_COLOR` e o fallback para locales sem UTF-8 mantêm legível a saída
redirecionada.

## Desenvolvimento

```bash
go test ./...
```

## Licença

[MIT](LICENSE)
