# ADR 012 : Entrée événementielle (CloudEvents)

**Statut** : Accepté

## Contexte

Avec l'API HTTP, le code métier décide lui-même d'envoyer un e-mail : il doit connaître Kourrier, les templates et les destinataires, puis appeler `POST /v1/emails` après avoir validé sa transaction. Deux problèmes en découlent :

- **Couplage** : chaque nouvel e-mail impose une modification du service métier.
- **Double écriture** : « commit SQL puis appel HTTP » n'est pas atomique. Si le Pod meurt entre les deux, l'e-mail est perdu. Si l'ordre est inversé, un e-mail part pour une transaction annulée. L'`Idempotency-Key` ([ADR 007](0007-idempotence.md)) ne couvre pas ce cas.

Les architectures microservices matures publient plutôt des **événements de domaine** (`user.signed_up`, `order.paid`) sur un bus, souvent via le *Transactional Outbox pattern* (Debezium, outbox applicatif), et un service dédié décide des notifications. Les solutions existantes sont soit du SaaS non souverain (Courier, Knock), soit des plates-formes lourdes (Novu : MongoDB, Redis et plusieurs services).

## Décision

Kourrier consomme des **CloudEvents 1.0** (projet gradué de la CNCF) et les transforme en e-mails grâce à des routes déclaratives ([ADR 013](0013-routage-declaratif-cel.md)). Deux transports sont livrés :

- **Push HTTP** : `POST /v1/events` (modes *structured* et *binary*), authentifié par la clé API du tenant (`Authorization: Bearer`). Il convient à tout producteur capable d'envoyer cet en-tête : SDK CloudEvents, Argo Events, relais maison. Un Trigger Knative ne sait pas ajouter d'en-tête ; il demandera le support de l'identité OIDC de Knative (voir les limites dans [events.md](../events.md#limites-connues)).
- **Pull NATS JetStream** : consommateurs durables ([ADR 014](0014-nats-jetstream.md)).

Principes :

1. **Un seul chemin vers la file.** Le service `internal/submit` est partagé par l'API et les événements. Validation, expéditeurs autorisés, idempotence et statuts sont donc strictement identiques.
2. **Isolation multi-tenant.** Un événement n'est évalué qu'avec les routes de *son* tenant : celui de la clé API (HTTP) ou celui auquel la source est liée dans le fichier de routes (NATS). Un producteur ne peut pas déclencher les routes d'un autre tenant.
3. **Au moins une fois, sans doublon.** Chaque e-mail issu d'un événement est soumis avec une clé d'idempotence `route + source + id`. Selon la spécification CloudEvents, le couple `(source, id)` identifie un événement de façon unique. Une redélivrance, ou un nouvel essai après un échec partiel, ne produit pas de doublon pendant le TTL d'idempotence. Les clés d'événements vivent dans un espace de noms distinct (`<tenant>:events`) de celles de l'API.
4. **Acquittement après persistance.** L'événement n'est acquitté qu'une fois les e-mails écrits dans Redis. Les résultats sont classés en quatre issues, que chaque transport traduit :

   | Issue | Signification | HTTP | NATS |
   |-------|---------------|------|------|
   | `accepted` | au moins un e-mail en file | `202` | `Ack` |
   | `ignored` | aucune route | `202` | `Ack` |
   | `rejected` | erreur permanente (événement invalide, route inévaluable) | `4xx` | `Term` |
   | `retry` | erreur transitoire (Redis, backpressure) | `503` + `Retry-After` | `NakWithDelay` |

5. **Backpressure.** Au-delà de `KOURRIER_EVENTS_MAX_QUEUE_DEPTH` e-mails en attente, l'ingestion se met en pause (`retry`). L'arriéré reste dans le broker, conçu pour cela et persistant sur disque, au lieu de gonfler Redis, qui est en mémoire.
6. **Parseur strict et borné.** Seuls les événements JSON en `specversion` 1.0 sont acceptés, avec une taille bornée par attribut et en nombre d'extensions. Le mode batch et `data_base64` sont refusés.

Le SDK officiel `cloudevents/sdk-go` n'est pas utilisé : le sous-ensemble nécessaire tient en environ 300 lignes testées et fuzzées, alors que le SDK ajoute `json-iterator` et `reflect2` (usage intensif d'`unsafe`). Cela réduit la surface d'attaque et les dépendances transitives.

## Conséquences

- **Positif** : le code métier ne connaît plus l'e-mail. Ajouter une notification revient à ajouter une route (revue en PR, testée en CI avec `kourrier routes test`). Combiné à un outbox côté producteur, l'envoi devient cohérent avec la transaction métier.
- **Positif** : l'API HTTP existante est inchangée ; les deux entrées cohabitent.
- **Négatif** : l'idempotence est bornée par `KOURRIER_IDEMPOTENCY_TTL` (24 h). Un événement rejoué au-delà (rejeu complet d'un stream) produit de nouveaux e-mails. C'est documenté dans [events.md](../events.md).
- **Négatif** : la livraison est non ordonnée, ce qui convient à l'e-mail.
- **Négatif** : un producteur compromis peut faire envoyer des e-mails à n'importe quelle adresse via une route dont le destinataire vient de l'événement. Les garde-fous sont les expéditeurs autorisés, la limite de destinataires et le rate limiting ; le modèle de menace est décrit dans [events.md](../events.md#sécurité).
