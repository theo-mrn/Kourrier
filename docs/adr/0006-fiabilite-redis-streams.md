# ADR 006 : Fiabilité de la file (Redis Streams, retries et dead-letter queue)

**Statut** : Accepté

## Contexte

Une file Redis naïve (`LPUSH` / `BRPOP`) retire le message dès sa lecture : si le Pod plante pendant l'envoi, l'e-mail est perdu. Il faut aussi retenter les erreurs transitoires sans bloquer la file, et isoler les messages qui ne pourront jamais partir.

## Décision

La file repose sur **Redis Streams avec un consumer group**, ce qui donne une livraison **au moins une fois** (*at-least-once*) :

| Clé | Type | Rôle |
|-----|------|------|
| `{kourrier}:stream` | Stream | Jobs prêts, lus par le groupe `workers` (`XREADGROUP`). |
| `{kourrier}:delayed` | Sorted set | Jobs en attente de retry, triés par date d'échéance. |
| `{kourrier}:dead` | Stream (plafonné) | Dead-letter queue. |

Cycle de vie d'un job :

1. Le worker lit le job, qui entre dans la *pending list* du groupe.
2. **Succès** : `XACK` + `XDEL`, dans une transaction.
3. **Erreur transitoire** : `Attempts++`, puis un script Lua déplace atomiquement le job dans `delayed`, avec un backoff exponentiel et un jitter (10 s, 20 s, 40 s… plafonné à 30 min, 8 tentatives par défaut).
4. **Erreur permanente ou tentatives épuisées** : déplacement atomique dans `dead`.
5. Un scheduler, présent dans chaque Pod, remet les jobs échus de `delayed` dans le stream. Le script Lua est idempotent, plusieurs Pods peuvent l'exécuter.
6. **Crash d'un Pod** : un job non acquitté au-delà de `KOURRIER_QUEUE_VISIBILITY_TIMEOUT` (5 min) est repris par un autre consumer (`XAUTOCLAIM`).
7. **Arrêt propre (SIGTERM)** : les envois en cours se terminent. Un job qui attendait le rate limiter est remis en file immédiatement, sans compter de tentative.

**Anti-doublon côté worker** : avant d'envoyer, le worker consulte le statut du job. S'il vaut déjà `sent` (ack perdu ou crash juste après l'envoi), il acquitte sans renvoyer. La fenêtre de doublon se réduit ainsi à l'intervalle entre l'appel au fournisseur et l'écriture du statut.

La DLQ s'exploite avec la CLI : `kourrier dlq list` et `kourrier dlq replay`.

## Conséquences

- **Positif** : aucune perte d'e-mail sur crash, retries non bloquants, poison messages isolés, compatible Redis Cluster (hash tag).
- **Négatif** : une garantie *exactly-once* stricte reste impossible face à un fournisseur externe : un doublon reste possible dans la fenêtre décrite plus haut. `KOURRIER_QUEUE_VISIBILITY_TIMEOUT` doit rester supérieur au temps de traitement maximal (vérifié au démarrage).
