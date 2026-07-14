# Image Optimizer

API Go híbrida: upload de imagens → MinIO (S3) + Postgres → variantes AVIF sob demanda (GET/HEAD síncrono via imgproxy; worker opcional para warm/async).

## Subir a stack

```bash
docker compose up --build
```

A API fica em `http://localhost:8080`. Serviços: `api`, `worker`, `postgres`, `rabbitmq`, `minio`, `imgproxy` (só rede interna), `prometheus`, `grafana`.

Compose local usa `MTLS_ENABLED=false` (um único `HTTP_ADDR` com todas as rotas).

| Serviço | URL |
| --- | --- |
| API | [http://localhost:8080](http://localhost:8080) |
| MinIO S3 API | [http://localhost:9000](http://localhost:9000) |
| MinIO Console | [http://localhost:9001](http://localhost:9001) (`minioadmin`/`minioadmin`) |
| Prometheus | [http://localhost:9090](http://localhost:9090) |
| Grafana | [http://localhost:3002](http://localhost:3002) (anônimo Viewer; admin `admin`/`admin`) |

Storage S3/MinIO: [`docs/minio.md`](docs/minio.md). Grafana: [`docs/grafana.md`](docs/grafana.md). Readiness (`/health` + `/ready`): [`docs/readiness.md`](docs/readiness.md). Retry backoff: [`docs/retry-backoff.md`](docs/retry-backoff.md).

Worker retry: max 3 attempts; exponential delays 1s then 2s before requeue (no sleep on final `MarkFailed`). O path público **nunca** devolve `202` — cold miss gera AVIF inline.

## Health e readiness

| Endpoint | Onde | Comportamento |
| --- | --- | --- |
| `GET /health` | API `:8080`, worker metrics `:9091` | Liveness: sempre `200` `{"status":"ok"}` (sem tocar deps) |
| `GET /ready` | API `:8080`, worker metrics `:9091` | Readiness: ping Postgres + RabbitMQ → `200` se ok, `503` se algum fail |

Compose usa `/ready` como healthcheck de `api` e `worker` (só ficam healthy com deps up). Detalhes: [`docs/readiness.md`](docs/readiness.md).

### Smoke readiness (manual)

```bash
docker compose up --build
curl -s localhost:8080/health   # 200
curl -s localhost:8080/ready    # 200 + checks ok
curl -s localhost:9091/ready    # 200
docker compose stop postgres
curl -s localhost:8080/ready    # 503; /health ainda 200
docker compose start postgres   # /ready volta a 200
```

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
| `HTTP_ADDR` | `:8080` | Listener público (`GET`/`HEAD`/`health`/`ready`/`metrics`; sem mTLS também `POST`/`DELETE`) |
| `WRITE_HTTP_ADDR` | *(obrigatório se mTLS)* | Listener de escrita (`POST`/`DELETE`) com mTLS |
| `METRICS_ADDR` | *(vazio = off)* | Endereço `/metrics`, `/health`, `/ready` do worker (Compose: `:9091`) |
| `MAX_UPLOAD_BYTES` | `10485760` | Limite de upload (10MB) |
| `RETRY_AFTER_SECONDS` | `2` | Reservado (legado); GET/HEAD de imagem não usam `202` |
| `DEFAULT_QUALITY` | `80` | Qualidade AVIF padrão |
| `SYNC_TRANSFORM_TIMEOUT` | `25s` | Timeout do transform inline no cold miss |
| `MTLS_ENABLED` | `false` | `true` → dual-listen (público + write com mTLS) |
| `TLS_CERT_FILE` | — | Certificado do servidor write (obrigatório se mTLS) |
| `TLS_KEY_FILE` | — | Chave do servidor write (obrigatório se mTLS) |
| `TLS_CLIENT_CA_FILE` | — | CA dos clients mTLS (obrigatório se mTLS) |

### mTLS dual-listen

Quando `MTLS_ENABLED=true`:

- `HTTP_ADDR` — rotas públicas: `GET`/`HEAD /images/:id`, `/health`, `/ready`, `/metrics` (plain HTTP ou atrás de proxy TLS)
- `WRITE_HTTP_ADDR` — `POST`/`DELETE /images` com `tls.RequireAndVerifyClientCert`

Local (Compose): `MTLS_ENABLED=false` → um só `HTTP_ADDR` com todas as rotas.

## Observabilidade (Prometheus + Grafana)

Métricas em texto Prometheus:

- API: `http://localhost:8080/metrics`
- Worker: `http://localhost:9091/metrics`

Config de scrape: [`deploy/prometheus.yml`](deploy/prometheus.yml) (jobs `api` → `api:8080`, `worker` → `worker:9091`).

Dashboard Grafana (Compose): [http://localhost:3002](http://localhost:3002) — [`docs/grafana.md`](docs/grafana.md).

### Métricas de cache (GET vs HEAD)

| Métrica | Método |
| --- | --- |
| `image_optimizer_cache_{hits,misses,pending,failed}_total` | GET |
| `image_optimizer_cache_head_{hits,misses,pending,failed}_total` | HEAD |

`misses` = cold miss com geração sync → `200`. `pending` = esperou variante in-flight → `200`. `jobs_enqueued_total` = enqueue best-effort (warm/async).

### PromQL de exemplo

Hit rate (cache GET):

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
3. Upload + GET (cold miss → `200` AVIF na 1ª chamada):
   ```bash
   ID=$(curl -s -X POST http://localhost:8080/images \
     -F "file=@./sample.jpg" \
     -F "folder=storely/1/catalog" | jq -r .id)
   curl -s -D - -o out.avif "http://localhost:8080/images/${ID}?w=300&h=200&fit=cover&crop=center&q=80"
   # esperado: HTTP/1.1 200 + Content-Type: image/avif
   ```
4. Em Graph / Explore, confirmar séries (ex. `image_optimizer_cache_misses_total`, `image_optimizer_jobs_enqueued_total`, `image_optimizer_jobs_processed_total`, `image_optimizer_queue_depth`)

## Exemplos curl

### Health / ready

```bash
curl -s http://localhost:8080/health
curl -s http://localhost:8080/ready
curl -s http://localhost:9091/health
curl -s http://localhost:9091/ready
```

### Upload

Multipart com `file` + `folder` (prefixo `storely/`, kinds: `catalog` / `avatars` / `themes` / `panel/admins`).

Object key no MinIO: `{folder}/{uuid}.{ext}` (ex. `storely/1/catalog/<uuid>.jpg`).

```bash
curl -s -X POST http://localhost:8080/images \
  -F "file=@./sample.jpg" \
  -F "folder=storely/1/catalog"
```

Resposta `201`:

```json
{"id":"<uuid>","content_type":"image/jpeg","size":12345}
```

### GET (sync → 200 AVIF)

Cold miss gera a variante inline (imgproxy) e responde `200` com body AVIF. Sem poll `202`.

```bash
ID=<uuid>
curl -s -o out.avif -w "%{http_code}\n" \
  "http://localhost:8080/images/${ID}?w=300&h=200&fit=cover&crop=center&q=80"
```

Sem params → reencode AVIF full-size (quality default):

```bash
curl -s -o full.avif -w "%{http_code}\n" "http://localhost:8080/images/${ID}"
```

### HEAD (sync → 200 sem baixar AVIF)

Mesmos query params do GET; cold miss também gera a variante, mas HEAD não devolve body:

```bash
ID=<uuid>
curl -sI "http://localhost:8080/images/${ID}?w=300&h=200&fit=cover&crop=center&q=80"
```

### Delete

```bash
curl -s -o /dev/null -w "%{http_code}\n" -X DELETE "http://localhost:8080/images/${ID}"
```

`204` = removido (objetos MinIO + rows Postgres).

### Copy (original bytes → novo id)

Copia o **original** (não a variante AVIF) para um novo UUID + `folder`:

```bash
curl -s -X POST -F "folder=storely/1/catalog" \
  "http://localhost:8080/images/${ID}/copy"
```

Resposta `201` igual ao upload: `{ id, content_type, size }`.

## Binário

Um binário, dois modos:

```bash
./app serve   # API HTTP + migrations no boot
./app worker  # consome image.variants → imgproxy → MinIO (warm/async opcional)
```
