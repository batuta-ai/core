# Batuta Core

> *Quem rege não toca.*

O núcleo Go do Batuta reúne as partes determinísticas do ciclo de condução:
roteamento, entregas, worktrees, execução, verificações, diário e integração.
Os hosts usam o binário deste módulo em vez de reimplementar essas regras.

## Uso do binário

```text
batuta loop --dry-run [<plano>]        mostra ondas e executores sem executar
batuta loop [<plano>]                  executa um plano aprovado
batuta loop --resume <entrega>         continua uma entrega interrompida
batuta loop --answer <tarefa> "<texto>" responde uma tarefa aguardando entrada
batuta loop --dashboard [<entrega>]    imprime um retrato TSV da entrega
batuta watch [<entrega>]               abre o painel interativo ao vivo
batuta trail [<entrega>]               mostra os registros do diário
```

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
