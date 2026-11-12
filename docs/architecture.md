# Architecture

Kourrier est un binaire Go unique ([ADR 001](adr/0001-go-monolithe-modulaire.md)). Redis, le stockage S3, le fournisseur d'e-mails et le broker d'événements sont des **dépendances externes**, en dehors du Pod.

L'architecture est hexagonale : un cœur unique (`submit` → file → workers) et des **adaptateurs d'entrée** interchangeables. Ce sont l'API HTTP, le sink CloudEvents et le consommateur NATS JetStream ([ADR 012](adr/0012-entree-evenementielle-cloudevents.md)). Tous passent par le même service de soumission.

```mermaid
flowchart LR
    client["Applications clientes<br/>(ex. yner.fr)"]
    bus[("NATS JetStream<br/>événements de domaine")]

    subgraph pod["Pod Kourrier (binaire Go unique)"]
        api["API HTTP :8080<br/>/v1/emails · /v1/events · /v1/templates"]
        cons["Consommateur JetStream"]
        disp["Dispatcher<br/>routes CEL · backpressure"]
        submit["submit<br/>validation · template · idempotence"]
        workers["Pool de workers<br/>rendu · rate limit · retries"]
        admin["Admin :9090<br/>/metrics · /livez · /readyz"]
        console["Console :8081 (optionnelle)<br/>SPA · API d'administration"]
    end

    redis[("Redis ≥ 7<br/>stream · delayed · dead<br/>idempotence · statuts · rate limit")]
    s3[("S3 / MinIO / RustFS<br/>templates")]
    provider["Fournisseur<br/>AWS SES · SMTP"]
    obs["Prometheus · Loki · Grafana"]

    client -- "POST /v1/emails → 202" --> api
    bus -- "pull · ack" --> cons
    api -- "CloudEvents" --> disp
    cons --> disp
    api --> submit
    disp --> submit
    submit -- "XADD" --> redis
    redis -- "XREADGROUP" --> workers
    workers -- "GetObject (cache TTL)" --> s3
    submit -. "template existe ? (cache)" .-> s3
    workers -- "Send" --> provider
    admin -. scrape .-> obs
    console -. "lecture · rejeu" .-> redis
    pod -. "logs JSON (stdout)" .-> obs
```

## Cycle de vie d'un e-mail

```mermaid
sequenceDiagram
    participant C as Client
    participant A as API
    participant S as S3
    participant R as Redis
    participant W as Worker
    participant P as Fournisseur

    C->>A: POST /v1/emails (Bearer, Idempotency-Key)
    A->>A: authentification, validation, expéditeur autorisé
    A->>S: le template existe et compile ? (si absent du cache)
    A->>R: SET NX idempotence · statut "queued" · XADD stream
    A-->>C: 202 {id, status: queued}
    W->>R: XREADGROUP (job en pending)
    W->>S: template (si absent du cache)
    W->>R: jeton du rate limiter (GCRA)
    W->>P: Send
    alt succès
        W->>R: statut "sent" · XACK + XDEL
    else erreur transitoire
        W->>R: statut "retrying" · ZADD delayed (backoff) · XACK
    else erreur permanente / tentatives épuisées
        W->>R: statut "failed" · XADD dead · XACK
    end
    C->>A: GET /v1/emails/{id}
    A-->>C: {status: sent, attempts, provider_message_id…}
```

## Packages

| Package | Rôle |
|---------|------|
| `cmd/kourrier` | Point d'entrée et CLI (`serve`, `keygen`, `dlq`, `routes`, `healthcheck`). |
| `internal/app` | Racine de composition : construit les composants depuis la configuration, gère le cycle de vie et l'arrêt propre. |
| `internal/config` | Configuration par variables d'environnement `KOURRIER_*`. |
| `internal/api` | API HTTP publique (`/v1/emails`, sink `/v1/events`) et serveur d'administration. |
| `internal/console` | Console d'administration web : API JSON, sessions signées et SPA embarquée ([ADR 016](adr/0016-console-d-administration.md)). Le front est dans `web/console`. |
| `internal/activity` | Fil plafonné des derniers changements de statut, lu par la console. |
| `internal/submit` | Service de soumission partagé par toutes les entrées : validation, expéditeur, idempotence, statut, mise en file. |
| `internal/cloudevent` | Parseur CloudEvents 1.0 strict et borné ([ADR 012](adr/0012-entree-evenementielle-cloudevents.md)). |
| `internal/routing` | Fichier de routes et expressions CEL compilées ([ADR 013](adr/0013-routage-declaratif-cel.md)). |
| `internal/dispatch` | Événement → e-mails : routage, idempotence par événement, issues, backpressure. |
| `internal/source/natsjs` | Consommateur NATS JetStream ([ADR 014](adr/0014-nats-jetstream.md)). |
| `internal/deadevent` | File des événements en échec, rejouable ([ADR 015](adr/0015-durcissement-entree-evenementielle.md)). |
| `internal/tenant` | Tenants, clés API, expéditeurs autorisés ([ADR 008](adr/0008-authentification-multi-tenant.md)). |
| `internal/queue` | Interface `Queue` et implémentation Redis Streams ([ADR 006](adr/0006-fiabilite-redis-streams.md)). |
| `internal/worker` | Pool de workers : rendu, rate limit, envoi, retries. |
| `internal/templates` | Chargement, cache et rendu des templates ([ADR 010](adr/0010-format-des-templates.md)). |
| `internal/storage` | Interface `storage.Provider` ; `s3store`, `fsstore` ([ADR 003](adr/0003-stockage-templates-s3.md)). |
| `internal/provider` | Interface `provider.Provider` ; `ses`, `smtp`, `console`, `filesystem`, `memory` ([ADR 004](adr/0004-abstraction-fournisseur.md)). |
| `internal/ratelimit` | Limiteurs GCRA Redis et local ([ADR 009](adr/0009-rate-limiting-distribue.md)). |
| `internal/idempotency` | Clés d'idempotence ([ADR 007](adr/0007-idempotence.md)). |
| `internal/status` | Statut de livraison par e-mail. |
| `internal/retry` | Classification permanente / transitoire et backoff. |
| `internal/observability` | Métriques Prometheus et logs ([ADR 005](adr/0005-observabilite-native.md)). |
| `internal/mail` | Types du domaine et validation. |
