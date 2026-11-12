# Configuration

Toute la configuration passe par des variables d'environnement préfixées `KOURRIER_`. Les durées utilisent la syntaxe Go (`30s`, `5m`, `24h`). Les incohérences sont détectées au démarrage.

## Général

| Variable | Défaut | Description |
|----------|--------|-------------|
| `KOURRIER_MODE` | `all` | `all` (API + workers), `api` ou `worker`. |
| `KOURRIER_HTTP_ADDR` | `:8080` | Adresse de l'API publique. |
| `KOURRIER_ADMIN_ADDR` | `:9090` | Adresse de `/metrics`, `/livez`, `/readyz`. |
| `KOURRIER_LOG_FORMAT` | `json` | `json` (Loki) ou `text`. |
| `KOURRIER_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `KOURRIER_SHUTDOWN_TIMEOUT` | `25s` | Délai d'arrêt propre (le `terminationGracePeriodSeconds` doit être supérieur). |
| `KOURRIER_MAX_BODY_BYTES` | `1048576` | Taille maximale d'une requête. |
| `KOURRIER_TENANTS_FILE` | `/etc/kourrier/tenants.yaml` | Fichier des tenants (obligatoire en mode `all`/`api`). |

## Redis

| Variable | Défaut | Description |
|----------|--------|-------------|
| `KOURRIER_REDIS_ADDR` | `localhost:6379` | Adresse Redis (>= 7). |
| `KOURRIER_REDIS_USERNAME` / `_PASSWORD` | | Identifiants ACL. |
| `KOURRIER_REDIS_DB` | `0` | Base. |
| `KOURRIER_REDIS_TLS` | `false` | Active TLS. |
| `KOURRIER_REDIS_KEY_PREFIX` | `kourrier` | Préfixe des clés (`{kourrier}:…`). |
| `KOURRIER_REDIS_WAIT_REPLICAS` | `0` | Nombre de réplicas devant acquitter chaque soumission (`WAIT`) avant confirmation. Protège d'une perte lors d'un basculement. `0` = désactivé. |
| `KOURRIER_REDIS_WAIT_TIMEOUT` | `500ms` | Attente maximale des réplicas ; au-delà, la soumission est rejetée comme transitoire (`503`). |

## File et workers

| Variable | Défaut | Description |
|----------|--------|-------------|
| `KOURRIER_QUEUE_VISIBILITY_TIMEOUT` | `5m` | Délai avant la reprise d'un job abandonné par un Pod mort. |
| `KOURRIER_QUEUE_RECLAIM_INTERVAL` | `30s` | Fréquence de recherche des jobs abandonnés. |
| `KOURRIER_QUEUE_SCHEDULER_INTERVAL` | `1s` | Fréquence de remise en file des retries échus. |
| `KOURRIER_QUEUE_DEAD_LETTER_MAX_LEN` | `10000` | Taille maximale (approximative) de la DLQ. |
| `KOURRIER_WORKER_CONCURRENCY` | `8` | Nombre de workers par Pod. |
| `KOURRIER_WORKER_SEND_TIMEOUT` | `30s` | Timeout d'un appel au fournisseur. |
| `KOURRIER_WORKER_MAX_ATTEMPTS` | `8` | Tentatives avant la DLQ. |
| `KOURRIER_WORKER_BACKOFF_BASE` | `10s` | Premier délai de retry (doublé à chaque tentative, avec jitter). |
| `KOURRIER_WORKER_BACKOFF_MAX` | `30m` | Délai de retry maximal. |
| `KOURRIER_RATE_LIMIT_PER_SECOND` | `10` | E-mails par seconde (`0` = illimité). |
| `KOURRIER_RATE_LIMIT_BURST` | `10` | Rafale autorisée. |
| `KOURRIER_RATE_LIMIT_SCOPE` | `global` | `global` (partagé via Redis) ou `local` (par Pod). |
| `KOURRIER_IDEMPOTENCY_TTL` | `24h` | Durée de mémorisation des `Idempotency-Key`. |
| `KOURRIER_STATUS_TTL` | `168h` | Durée de conservation des statuts. |
| `KOURRIER_ACTIVITY_MAX_LEN` | `10000` | Nombre approximatif d'entrées du fil d'activité (derniers changements de statut, destinataires masqués, [ADR 016](adr/0016-console-d-administration.md)). `0` le désactive. |

## Templates et stockage

| Variable | Défaut | Description |
|----------|--------|-------------|
| `KOURRIER_TEMPLATES_CACHE_TTL` | `1m` | Durée de cache des templates compilés (`0` = pas de cache). |
| `KOURRIER_TEMPLATES_DEFAULT_LOCALE` | `en` | Locale de repli. |
| `KOURRIER_STORAGE_PROVIDER` | `s3` | `s3` ou `filesystem`. |
| `KOURRIER_STORAGE_DIR` | `./templates` | Répertoire (provider `filesystem`). |
| `KOURRIER_STORAGE_S3_BUCKET` | `kourrier-templates` | Bucket. |
| `KOURRIER_STORAGE_S3_PREFIX` | | Préfixe des clés dans le bucket. |
| `KOURRIER_STORAGE_S3_REGION` | `us-east-1` | Région. |
| `KOURRIER_STORAGE_S3_ENDPOINT` | | Endpoint d'un service compatible S3 (MinIO, RustFS…). |
| `KOURRIER_STORAGE_S3_USE_PATH_STYLE` | `false` | `true` pour MinIO et la plupart des services compatibles. |
| `KOURRIER_STORAGE_S3_ACCESS_KEY_ID` / `_SECRET_ACCESS_KEY` | | Identifiants statiques (sinon chaîne AWS par défaut / IRSA). |

## Fournisseur d'envoi

| Variable | Défaut | Description |
|----------|--------|-------------|
| `KOURRIER_MAILER_PROVIDER` | `console` | `aws-ses`, `smtp`, `console`, `filesystem`. |
| `KOURRIER_MAILER_SES_REGION` | `eu-west-3` | Région SES. |
| `KOURRIER_MAILER_SES_CONFIGURATION_SET` | | Configuration set (événements bounce/complaint). |
| `KOURRIER_MAILER_SMTP_HOST` / `_PORT` | `localhost` / `1025` | Serveur SMTP. |
| `KOURRIER_MAILER_SMTP_USERNAME` / `_PASSWORD` | | Authentification (mécanisme négocié). |
| `KOURRIER_MAILER_SMTP_TLS` | `none` | `none`, `opportunistic`, `mandatory` (STARTTLS), `implicit` (port 465). |
| `KOURRIER_MAILER_SMTP_TIMEOUT` | `15s` | Timeout de connexion. |
| `KOURRIER_MAILER_FILESYSTEM_DIR` | `./tmp/emails` | Dossier de sortie du driver `filesystem`. |

## Entrée événementielle

Active quand `KOURRIER_EVENTS_ROUTES_FILE` est renseigné, dans les modes `all` et `api` ([événements](events.md)).

| Variable | Défaut | Description |
|----------|--------|-------------|
| `KOURRIER_EVENTS_ROUTES_FILE` | | Fichier de routes (sources et routes). Vide = entrée désactivée, `POST /v1/events` répond `404`. |
| `KOURRIER_EVENTS_MAX_BYTES` | `262144` | Taille maximale d'un événement (corps HTTP ou message NATS). |
| `KOURRIER_EVENTS_DEAD_LETTER_MAX_LEN` | `10000` | Taille maximale (approximative) de la file des événements en échec. |
| `KOURRIER_EVENTS_MAX_QUEUE_DEPTH` | `50000` | Pause de l'ingestion au-delà de ce nombre d'e-mails en attente (`ready` + `delayed`). `0` = désactivé. |
| `KOURRIER_EVENTS_NATS_URL` | `nats://localhost:4222` | Serveur(s) NATS, séparés par des virgules. Utilisé seulement si le fichier déclare des `sources.nats`. |
| `KOURRIER_EVENTS_NATS_CREDS_FILE` | | Fichier de credentials NATS (JWT + seed). Exclusif avec `_NKEY_FILE`. |
| `KOURRIER_EVENTS_NATS_NKEY_FILE` | | Fichier de seed NKey. |
| `KOURRIER_EVENTS_NATS_TLS_CA_FILE` | | CA du serveur NATS. |
| `KOURRIER_EVENTS_NATS_TLS_CERT_FILE` / `_KEY_FILE` | | Certificat client (mTLS), à fournir ensemble. |

## AWS SES en production

1. Vérifier le domaine d'envoi dans SES (DKIM, SPF, DMARC) et sortir du sandbox.
2. Créer un rôle IAM avec `ses:SendEmail` (et `s3:GetObject` / `s3:ListBucket` sur le bucket des templates), puis l'associer au ServiceAccount (IRSA : annotation `eks.amazonaws.com/role-arn`).
3. Optionnel : créer un configuration set qui publie les bounces et les plaintes (SNS/EventBridge). La suppression list du compte SES bloque déjà les adresses en échec.
4. Régler `rateLimit.perSecond` sur le quota d'envoi du compte.
