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

O painel mostra contexto, progresso, tarefas, verificações, commits e a saída
do executor. Use `--once` para imprimir um único quadro e `--interval` para
definir a frequência de consulta ao diário.

## Desenvolvimento

```bash
go test ./...
```

## Licença

[MIT](LICENSE)
