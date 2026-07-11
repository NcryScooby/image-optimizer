# Image Optimizer

API Go híbrida: upload de imagens → MinIO (S3) + Postgres → variantes AVIF sob demanda via worker + imgproxy + RabbitMQ.

## Subir a stack

```bash
docker compose up --build
```

A API fica em `http://localhost:8080`. Serviços: `api`, `worker`, `postgres`, `rabbitmq`, `minio`, `imgproxy` (só rede interna), `prometheus`, `grafana`.

| Serviço | URL |
| --- | --- |
| API | [http://localhost:8080](http://localhost:8080) |
| MinIO S3 API | [http://localhost:9000](http://localhost:9000) |
| MinIO Console | [http://localhost:9001](http://localhost:9001) (`minioadmin`/`minioadmin`) |
| Prometheus | [http://localhost:9090](http://localhost:9090) |
| Grafana | [http://localhost:3000](http://localhost:3000) (anônimo Viewer; admin `admin`/`admin`) |

Storage S3/MinIO: [`docs/minio.md`](docs/minio.md). Grafana: [`docs/grafana.md`](docs/grafana.md).

## Variáveis de ambiente

| Variável | Default | Descrição |
| --- | --- | --- |
| `DATABASE_URL` | *(obrigatório)* | URL Postgres (`postgres://...`) |
| `RABBITMQ_URL` | *(obrigatório)* | URL AMQP |
| `IMGPROXY_URL` | *(obrigatório)* | Base URL do imgproxy (ex. `http://imgproxy:8080`) |
| `S3_ENDPOINT` | *(obrigatório)* | Endpoint S3 (Compose: `http://minio:9000`) |
| `S3_REGION` | *(obrigatório)* | Região S3 (Compose: `us-east-1`) |
| `S3_BUCKET` | *(obrigatório)* | Bucket único (Compose: `images`) |
| `S3_ACCESS_KEY` | *(obrigatório)* | Access key MinIO/S3 |
| `S3_SECRET_KEY` | *(obrigatório)* | Secret key MinIO/S3 |
| `S3_USE_PATH_STYLE` | `false` | `true` para MinIO (path-style) |
| `HTTP_ADDR` | `:8080` | Endereço HTTP do `serve` (inclui `GET /metrics`) |
| `METRICS_ADDR` | *(vazio = off)* | Endereço `/metrics` do worker (Compose: `:9091`) |
| `MAX_UPLOAD_BYTES` | `10485760` | Limite de upload (10MB) |
| `RETRY_AFTER_SECONDS` | `2` | Header `Retry-After` em respostas `202` |
| `DEFAULT_QUALITY` | `80` | Qualidade AVIF padrão |

## Observabilidade (Prometheus + Grafana)

Métricas em texto Prometheus:

- API: `http://localhost:8080/metrics`
- Worker: `http://localhost:9091/metrics`

Config de scrape: [`deploy/prometheus.yml`](deploy/prometheus.yml) (jobs `api` → `api:8080`, `worker` → `worker:9091`).

Dashboard Grafana (Compose): [http://localhost:3000](http://localhost:3000) — [`docs/grafana.md`](docs/grafana.md).

### PromQL de exemplo

Hit rate (cache):

```promql
sum(rate(image_optimizer_cache_hits_total[5m]))
/
(sum(rate(image_optimizer_cache_hits_total[5m])) + sum(rate(image_optimizer_cache_misses_total[5m])))
```

Profundidade da fila:

```promql
image_optimizer_queue_depth
```

p99 duração do job no worker:

```promql
histogram_quantile(0.99, sum(rate(image_optimizer_worker_job_duration_seconds_bucket[5m])) by (le, result))
```

### Smoke checklist (manual)

1. `docker compose up --build`
2. Em [http://localhost:9090/targets](http://localhost:9090/targets), jobs `api` e `worker` em estado **UP**
3. Upload + GET (cold miss) + aguardar worker processar (poll até `200`):
   ```bash
   ID=$(curl -s -X POST http://localhost:8080/images -F "file=@./sample.jpg" | jq -r .id)
   curl -s -D - -o /dev/null "http://localhost:8080/images/${ID}?w=300&h=200&fit=cover&q=80"
   # repetir GET até 200
   ```
4. Em Graph / Explore, confirmar séries (ex. `image_optimizer_cache_misses_total`, `image_optimizer_jobs_enqueued_total`, `image_optimizer_jobs_processed_total`, `image_optimizer_queue_depth`, `image_optimizer_worker_job_duration_seconds_bucket`)

## Exemplos curl

### Health

```bash
curl -s http://localhost:8080/health
```

### Upload

```bash
curl -s -X POST http://localhost:8080/images \
  -F "file=@./sample.jpg"
```

Resposta `201`:

```json
{"id":"<uuid>","content_type":"image/jpeg","size":12345}
```

### GET (poll 202 → 200 AVIF)

Primeira chamada (ou enquanto o worker processa) → `202` + `Retry-After`:

```bash
ID=<uuid>
curl -s -D - -o /dev/null "http://localhost:8080/images/${ID}?w=300&h=200&fit=cover&q=80"
```

Repita até `200` e grave o AVIF:

```bash
curl -s -o out.avif -w "%{http_code}\n" "http://localhost:8080/images/${ID}?w=300&h=200&fit=cover&q=80"
```

Sem params → reencode AVIF full-size (quality default):

```bash
curl -s -o full.avif -w "%{http_code}\n" "http://localhost:8080/images/${ID}"
```

### Delete

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X DELETE "http://localhost:8080/images/${ID}"
```

`204` = removido (objetos MinIO + rows Postgres).

## Binário

Um binário, dois modos:

```bash
./app serve   # API HTTP + migrations no boot
./app worker  # consome image.variants → imgproxy → MinIO
```
