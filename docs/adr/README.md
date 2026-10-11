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
