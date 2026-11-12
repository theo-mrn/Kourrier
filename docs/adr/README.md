# Architecture Decision Records

Projet : microservice d'e-mailing asynchrone, cloud-native et agnostique.

| ADR | Titre | Statut |
|-----|-------|--------|
| [001](0001-go-monolithe-modulaire.md) | Langage et patron d'architecture (Go & monolithe modulaire) | Accepté |
| [002](0002-file-asynchrone-redis.md) | File d'attente asynchrone (Redis) | Accepté |
| [003](0003-stockage-templates-s3.md) | Gestion et stockage des templates (S3 / MinIO) | Accepté |
| [004](0004-abstraction-fournisseur.md) | Moteur d'envoi et abstraction du fournisseur | Accepté |
| [005](0005-observabilite-native.md) | Observabilité native (Prometheus, Loki, Grafana) | Accepté |
| [006](0006-fiabilite-redis-streams.md) | Fiabilité de la file : Redis Streams, retries et DLQ | Accepté |
| [007](0007-idempotence.md) | Idempotence des envois | Accepté |
| [008](0008-authentification-multi-tenant.md) | Authentification et isolation multi-tenant | Accepté |
| [009](0009-rate-limiting-distribue.md) | Rate limiting distribué | Accepté |
| [010](0010-format-des-templates.md) | Format, localisation et cache des templates | Accepté, amendé par 011 |
| [011](0011-templates-dans-l-api.md) | Validation, catalogue et prévisualisation des templates par l'API | Accepté |
| [012](0012-entree-evenementielle-cloudevents.md) | Entrée événementielle (CloudEvents) | Accepté |
| [013](0013-routage-declaratif-cel.md) | Routage déclaratif avec CEL | Accepté |
| [014](0014-nats-jetstream.md) | NATS JetStream comme premier broker | Accepté |
| [015](0015-durcissement-entree-evenementielle.md) | Durcissement de l'entrée événementielle | Accepté |
| [016](0016-console-d-administration.md) | Console d'administration web | Accepté |
