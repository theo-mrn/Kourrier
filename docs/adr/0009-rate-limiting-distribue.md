# ADR 009 : Rate limiting distribué

**Statut** : Accepté

## Contexte

Les fournisseurs imposent un débit maximal (AWS SES : 1 e-mail/s en sandbox, 14/s au départ en production, puis selon le quota du compte). Le dépasser provoque du throttling, voire nuit à la réputation. Avec plusieurs replicas, une limite par Pod multiplie le débit réel.

## Décision

Avant chaque envoi, le worker attend un jeton du `ratelimit.Limiter` :

- `global` (défaut) : algorithme **GCRA** exécuté dans un script Lua sur Redis. Une seule clé, l'horloge provient du serveur Redis (indépendante des décalages entre Pods), la limite est partagée par **tous** les Pods.
- `local` : token bucket en mémoire (`golang.org/x/time/rate`), par Pod.
- `KOURRIER_RATE_LIMIT_PER_SECOND=0` désactive la limite.

Le temps d'attente est exposé (`kourrier_rate_limit_wait_seconds`) : quand il grimpe, le quota est le goulot d'étranglement.

## Conséquences

- **Positif** : respect strict du quota quel que soit le nombre de replicas, rafales configurables (`BURST`).
- **Négatif** : un aller-retour Redis par e-mail (négligeable devant la latence du fournisseur).
