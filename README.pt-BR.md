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
versão, plataforma, modelo e esforço; o binário padrão ainda não inclui
lançamentos ACP aprovados. Uma tentativa ACP incerta é preservada para
reconciliação e nunca é repetida automaticamente via CLI. Subagentes nativos
pertencem ao host interativo, não ao binário. Consulte
[dispatch](docs/dispatch.md) e o
[protocolo de medição](docs/dispatch-measurement.md).

A [supervisão do loop](docs/loop-supervision.md) acompanha uma entrega explícita
com cursor durável. O host precisa manter o processo em execução; notificações
por arquivo local ou desktop compatível são opt-in, e sem um destino os eventos
permanecem não lidos. A observação não faz chamadas a modelos. Após a conclusão,
o supervisor executa uma revisão completa da entrega imutável mesmo sem
`--policy`; concluir apenas o loop comum não inicia essa revisão. Uma política
pode fornecer a resposta fixa e limitada ao escopo da tarefa ou reservar uma
proposta de correção explicitamente autorizada. Conclusão da implementação,
resultado da revisão e aceitação pelo condutor são estados distintos. A evidência
fica em `.batuta/reviews/supervision/<job-id>/`, e a saída do motor em
`artifacts/`. A CLI de supervisão não oferece uma opção de timeout da revisão,
portanto usa o padrão fixo de uma hora. Cancelamento ou timeout enquanto aguarda
a aquisição da posse retorna um erro sem transição do job. Uma interrupção da
sondagem ou do snapshot resulta em `failed`/`execution_failed`; após o lançamento
do motor, resulta em `uncertain`/`cleanup_unresolved`, sem repetição automática.

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
