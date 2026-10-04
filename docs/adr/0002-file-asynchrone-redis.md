# ADR 002 : Gestion de la file d'attente asynchrone (Redis)

**Statut** : Accepté

## Contexte

Les applications clientes (ex. yner.fr) ne doivent jamais être bloquées par la latence réseau ou les limitations d'un fournisseur d'e-mailing externe.

## Décision

Redis (>= 7) sert de broker asynchrone. L'API valide la requête, la pousse dans Redis et répond immédiatement `202 Accepted`. Un pool de workers dépile la file au rythme autorisé (voir [ADR 009](0009-rate-limiting-distribue.md)). La structure exacte de la file est décrite dans l'[ADR 006](0006-fiabilite-redis-streams.md).

Redis héberge aussi les clés d'idempotence ([ADR 007](0007-idempotence.md)), les statuts d'envoi (`GET /v1/emails/{id}`) et le compteur du rate limiter. Toutes les clés partagent le préfixe `{kourrier}` : ce hash tag les place dans le même slot en Redis Cluster, ce qui rend les scripts Lua multi-clés compatibles.

## Conséquences

- **Positif** : découplage total, absorption des pics de trafic, résilience face aux pannes transitoires.
- **Négatif** : dépendance externe (Redis), standard sur Kubernetes. La persistance AOF (`appendonly yes`) doit être activée pour ne pas perdre la file au redémarrage de Redis.
