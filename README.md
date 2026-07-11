# Image Optimizer

API Go híbrida: upload de imagens → disco + Postgres → variantes AVIF sob demanda via worker + imgproxy + RabbitMQ.

## Subir a stack

```bash
docker compose up --build
```

A API fica em `http://localhost:8080`. Serviços: `api`, `worker`, `postgres`, `rabbitmq`, `imgproxy` (só rede interna).

## Variáveis de ambiente

| Variável | Default | Descrição |
| --- | --- | --- |
| `DATABASE_URL` | *(obrigatório)* | URL Postgres (`postgres://...`) |
| `RABBITMQ_URL` | *(obrigatório)* | URL AMQP |
| `IMGPROXY_URL` | *(obrigatório)* | Base URL do imgproxy (ex. `http://imgproxy:8080`) |
| `DATA_DIR` | `/data` | Raiz do volume de originais/variantes |
| `HTTP_ADDR` | `:8080` | Endereço HTTP do `serve` |
| `MAX_UPLOAD_BYTES` | `10485760` | Limite de upload (10MB) |
| `RETRY_AFTER_SECONDS` | `2` | Header `Retry-After` em respostas `202` |
| `DEFAULT_QUALITY` | `80` | Qualidade AVIF padrão |

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

`204` = removido (disco + rows).

## Binário

Um binário, dois modos:

```bash
./app serve   # API HTTP + migrations no boot
./app worker  # consome image.variants → imgproxy → disco
```
