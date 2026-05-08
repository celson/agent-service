---
name: collect-metrics
description: Coleta os 4 golden signals e logs do serviço afetado via Grafana
---

## Skill: Coleta de Métricas

Execute os passos abaixo em ordem para coletar evidências do serviço afetado.

### 1. Descoberta de métricas
Use `list_prometheus_metric_names` para listar as métricas disponíveis.
Filtre pelo nome do serviço para encontrar as métricas relevantes.

### 2. Os 4 Golden Signals (Prometheus)

**Latência** — tempo de resposta das requisições:
```
histogram_quantile(0.99, rate(<metric>_duration_seconds_bucket{service="<service>"}[5m]))
histogram_quantile(0.50, rate(<metric>_duration_seconds_bucket{service="<service>"}[5m]))
```

**Taxa de erros** — proporção de requisições com falha:
```
rate(<metric>_requests_total{service="<service>", status=~"5.."}[5m])
  /
rate(<metric>_requests_total{service="<service>"}[5m])
```

**Saturação** — uso de recursos (CPU, memória, filas):
```
rate(process_cpu_seconds_total{service="<service>"}[5m])
process_resident_memory_bytes{service="<service>"}
```

**Tráfego** — volume de requisições:
```
rate(<metric>_requests_total{service="<service>"}[5m])
```

### 3. Logs do serviço (Loki)
Busque erros e warnings no período do incidente:
```logql
{service_name="<service>"} |= "error" | json | line_format "{{.timestamp}} {{.level}} {{.message}}"
```

Busque por status codes de erro:
```logql
{service_name="<service>"} | json | status >= 500
```

### 4. Anotações de deploys
Use `get_annotations` para verificar se houve deploy ou mudança de configuração no período.

### Saída esperada desta skill
Ao final, você deve ter coletado:
- Valores dos 4 golden signals no período do incidente
- Logs de erro relevantes com timestamps
- Lista de deploys/eventos próximos ao início do incidente
