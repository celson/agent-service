---
name: troubleshooting
description: Correlaciona métricas coletadas e produz um RCA estruturado
uses: collect-metrics
---

## Skill: Troubleshooting e RCA

Esta skill depende de `collect-metrics`. Execute-a primeiro, depois aplique os passos abaixo.

### 1. Correlação temporal
Com os dados coletados, identifique:
- Em qual momento exato as métricas começaram a se degradar?
- A degradação foi gradual ou abrupta?
- Houve algum deploy/anotação próximo ao início da degradação?

### 2. Correlação entre sinais
Analise as relações de causa e efeito:
- Alta latência acompanhou aumento de erros? → Possível sobrecarga ou deadlock
- Taxa de erros subiu sem aumento de latência? → Possível falha de dependência externa
- CPU/memória saturada antes dos erros? → Possível vazamento de recursos
- Tráfego aumentou antes da degradação? → Possível problema de capacidade

### 3. Análise dos logs
Nos logs coletados:
- Qual foi a primeira mensagem de erro (timestamp mais antigo)?
- Há stack traces? Qual exceção está sendo lançada?
- Os erros apontam para um componente específico (DB, cache, API externa)?

### 4. Os 5 Whys
Aplique a metodologia partindo do sintoma observado:
1. Por que o alerta disparou? → [sintoma]
2. Por que [sintoma] aconteceu? → [causa imediata]
3. Por que [causa imediata] aconteceu? → [causa intermediária]
4. Por que [causa intermediária] aconteceu? → [causa sistêmica]
5. Por que [causa sistêmica] existe? → [causa raiz]

### 5. Formato de saída obrigatório

## Root Cause
[Causa raiz com evidências: métricas, logs, timestamps]

## Impact
[Serviços afetados | Usuários impactados | Duração]

## Timeline
| Timestamp | Evento |
|-----------|--------|
| HH:MM     | Primeiro sinal de degradação nas métricas |
| HH:MM     | Primeiro erro nos logs |
| HH:MM     | Alerta disparou |

## Remediation Steps
**Imediato:**
- [ação para mitigar agora]

**Longo prazo:**
- [ação para prevenir recorrência]
