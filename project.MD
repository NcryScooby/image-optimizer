```markdown
# Prompt: API Go — Image Optimizer (híbrido + imgproxy + RabbitMQ)

## Objetivo

Criar do zero uma API em Go que recebe uploads de imagem, persiste no disco e serve variantes AVIF sob demanda. Transformações via **imgproxy** (serviço interno). Processamento assíncrono via **RabbitMQ**. Cache de variantes no disco com deduplicação no **Postgres**.

Projeto atual: repositório vazio (`image-optimizer`).

## Arquitetura (decidida)

### Fluxo híbrido

1. `POST` grava **original** no disco + row em `images`.
2. `GET /images/{id}?w=&h=&crop=&q=&fit=` :
   - Se variante `ready` → serve arquivo AVIF do disco (`200`, `Content-Type: image/avif`).
   - Se não existe → cria row `variants` (`pending`), publica job no RabbitMQ, responde **`202 Accepted`** + header **`Retry-After`**.
   - Se `pending` → `202` + `Retry-After` (não republica job).
   - Se `failed` após esgotar retries → `422` JSON com erro.
3. **Worker** consome fila, chama **imgproxy** (HTTP interno), grava AVIF no disco, atualiza variante para `ready`.

### Papel do imgproxy

- Go na frente (API pública).
- imgproxy atrás, só rede Docker interna, lê originais do **volume local compartilhado**.
- Go traduz query params → URL/opções imgproxy; worker busca resultado e persiste no cache de variantes.
- Output **sempre AVIF** no v1.

### Processos

- **Um binário**, dois modos: `serve` (API) e `worker` (ex.: flag/subcommand ou `APP_MODE`).
- Docker Compose sobe `api` e `worker` com a mesma image, comandos diferentes.

## Stack

| Peça       | Escolha                                                               |
| ---------- | --------------------------------------------------------------------- |
| HTTP       | Go 1.22+, **chi**                                                     |
| DB         | **Postgres** + **pgx** + SQL cru                                      |
| Migrations | **goose** (SQL)                                                       |
| Fila       | **RabbitMQ** (`amqp091-go`)                                           |
| Transform  | **imgproxy** (container oficial)                                      |
| Storage    | Disco local, volume compartilhado Compose                             |
| Auth       | **Nenhuma** no v1 (rede local/Compose)                                |
| Testes     | **Unitários** (parse params, cache key/hash, mapeamento URL imgproxy) |

## Endpoints v1

- `POST /images` — `multipart/form-data`, campo `file`
- `GET /images/{id}` — query opcional; sem params = reencode AVIF full-size (quality default)
- `DELETE /images/{id}` — apaga original, variantes no disco e rows
- `GET /health` — liveness (e, se simples, ping DB/Rabbit)

### Upload

- Formatos: **JPEG, PNG, WebP**
- Max: **10MB** (env `MAX_UPLOAD_BYTES` configurável)
- Resposta `201` JSON: `{ "id": "<uuid>", "content_type": "...", "size": N }`

### Query params (GET)

| Param  | Significado | Valores                            |
| ------ | ----------- | ---------------------------------- |
| `w`    | width       | int > 0                            |
| `h`    | height      | int > 0                            |
| `crop` | âncora      | `center\|top\|bottom\|left\|right` |
| `q`    | quality     | 1–100 (default ex. 80)             |
| `fit`  | modo        | `cover\|contain\|fill`             |

- Sem params → variante “identity”: AVIF full-size com defaults.
- Cache key = hash estável/ordenado dos params normalizados (+ defaults aplicados).
- Unique no Postgres: `(image_id, params_hash)`.

### Contratos de status (GET)

| Situação                   | HTTP  | Notas                     |
| -------------------------- | ----- | ------------------------- |
| Pronta                     | `200` | body = bytes AVIF         |
| Pendente / 1ª vez          | `202` | `Retry-After: <segundos>` |
| Failed (retries esgotados) | `422` | JSON erro                 |
| Imagem inexistente         | `404` |                           |
| Params inválidos           | `400` |                           |

### Falhas do worker

- Max **3 retries** com reenqueue/backoff.
- Depois: status `failed`; GET → `422`.

## Modelo de dados (mínimo)

**images:** `id UUID PK`, `original_path`, `content_type`, `size_bytes`, `created_at`  
**variants:** `id UUID PK`, `image_id FK`, `params_hash`, `params_json`, `status` (`pending|ready|failed`), `path`, `attempts`, `last_error`, `created_at`, `updated_at`

- Unique `(image_id, params_hash)`

## Layout de disco (sugestão)
```

/data/originals/{id}.{ext}
/data/variants/{id}/{params_hash}.avif

```
Volume montado em api, worker e imgproxy.

## Docker Compose (obrigatório)
Serviços: `api`, `worker`, `postgres`, `rabbitmq`, `imgproxy`
- Volume compartilhado `./data` (ou named volume)
- imgproxy **sem** porta pública (só rede interna)
- API expõe porta HTTP (ex. 8080)
- Env documentado: DB URL, Rabbit URL, imgproxy base URL, paths, `MAX_UPLOAD_BYTES`, `RETRY_AFTER_SECONDS`, quality default

## Estrutura do código (sugestão)
```

cmd/app/ # main: serve | worker
internal/http/ # chi routes, handlers
internal/storage/ # disco
internal/db/ # pgx repos
internal/queue/ # publish/consume
internal/imgproxy/# monta URL + client HTTP
internal/transform/ # parse/validate/normalize params + hash
migrations/ # goose SQL
docker-compose.yml
Dockerfile
README.md

```

## Critérios de sucesso
1. `docker compose up --build` sobe stack.
2. Upload JPEG/PNG/WebP ≤10MB → `201` + id.
3. Primeiro GET com params → `202`; após worker → mesmo GET → `200` AVIF.
4. Segundo GET idêntico durante `pending` → `202` **sem** duplicar job (unique + check status).
5. GET sem params → AVIF full-size (via fila/cache).
6. DELETE remove arquivos + rows.
7. Worker falha 3x → `422` no GET.
8. Testes unitários cobrem hash/params/mapeamento imgproxy.
9. Sem auth; imgproxy não exposto publicamente.

## Fora de escopo (v1)
- API keys/JWT, S3/MinIO, listagem paginada, GIF animado, formatos de saída ≠ AVIF, CDN, signed URLs imgproxy (a menos que imgproxy exija key interna — aí só rede privada + key de config), testcontainers/E2E automatizado.

## Instruções de implementação
- Implementar completo e rodável via Compose.
- Código idiomático Go; erros tratados; logs estruturados simples (stdout).
- README: como subir, exemplos `curl` (upload, GET polling 202→200, delete).
- Não adicionar features fora do escopo acima.
```
