# ADR 015 : Durcissement de l'entrée événementielle

**Statut** : Accepté

## Contexte

Une revue de l'entrée événementielle ([ADR 012](0012-entree-evenementielle-cloudevents.md)) a relevé plusieurs risques qu'un premier utilisateur rencontrerait :

1. Une clé API de producteur d'événements pouvait aussi envoyer des e-mails arbitraires via `POST /v1/emails`.
2. Un tenant qui inonde Kourrier d'événements remplit la file partagée et met en pause l'ingestion de tous les autres.
3. Un événement rejeté (route boguée, template manquant) était terminé côté broker, donc perdu, même une fois la route corrigée. Il en allait de même des routes en échec lors d'un échec partiel.
4. Une faute de frappe dans un type d'événement passait inaperçue : l'événement était simplement « ignoré ».
5. Un événement de 1 Mio correspondant à 10 routes crée 10 Mio de jobs dans Redis.
6. Un JSON avec des clés en double (`{"id":"a","id":"b"}`) est lu différemment selon les parseurs.
7. Un basculement Redis entre l'écriture sur le primaire et la réplication perd un événement déjà acquitté au broker.

## Décision

| Risque | Décision |
|--------|----------|
| 1 | **Scopes par clé API** : `emails` (`POST /v1/emails`) et `events` (`POST /v1/events`). Lire un statut est permis à toute clé du tenant. Une clé sans `scopes` garde les deux, par compatibilité ; `kourrier keygen -scopes events` produit une clé dédiée. Une clé hors scope reçoit `403 insufficient-scope`. |
| 2 | **Débit d'e-mails par tenant à l'ingestion** (`limits` dans le fichier de routes), en GCRA sur Redis comme le rate limiting d'envoi ([ADR 009](0009-rate-limiting-distribue.md)), mais non bloquant : au-delà, l'événement est redélivré plus tard (`429` + `Retry-After` en HTTP, `NakWithDelay` en NATS). |
| 3 | **File des événements en échec** (`{prefix}:events:dead`, stream plafonné). Kourrier y conserve les événements rejetés, la liste des routes en échec d'un échec partiel et les charges utiles NATS invalides. `kourrier events replay` ne rejoue que les routes en échec ; s'il échoue encore, l'événement est reconservé. **Si la conservation échoue, l'événement est redélivré** au lieu d'être acquitté : Kourrier ne perd rien en silence. |
| 4 | `on_unrouted: dead_letter` par source NATS : un événement sans route est conservé, avec une alerte sur la taille de la file. Le mode par défaut reste `ignore`, car un stream partagé contient légitimement des types non routés. |
| 5 | **Taille maximale d'un événement** (`KOURRIER_EVENTS_MAX_BYTES`, 256 Kio par défaut). |
| 6 | **Clés JSON en double et imbrication au-delà de 64 niveaux refusées**, dans les attributs comme dans les données. |
| 7 | **`KOURRIER_REDIS_WAIT_REPLICAS`** : la soumission (et la conservation d'un événement en échec) n'est confirmée qu'après l'acquittement de N réplicas (`WAIT`). Le chemin d'idempotence attend aussi, puisque l'écriture d'origine peut ne pas être répliquée. Désactivé par défaut, car cela coûte de la latence. |

Compromis assumés et documentés plutôt que corrigés :

- **Renommer une route** change ses clés d'idempotence : un événement redélivré dans le TTL produit un second e-mail.
- **Recréation d'un consommateur supprimé** en `DeliverNew` : les événements publiés pendant la coupure ne sont pas rejoués.
- **Rolling update** qui change `filter_subjects` : les anciens et nouveaux Pods mettent à jour le consommateur tour à tour jusqu'à la fin du déploiement. Utilisez `bind: true` en production pour gérer le consommateur de façon déclarative.

## Conséquences

- **Positif** : moindre privilège pour les producteurs, équité entre tenants, aucune perte silencieuse d'événement, détection des erreurs de configuration.
- **Négatif** : la file des événements en échec contient des données personnelles. Elle est plafonnée, vidée au rejeu, et doit être surveillée (alerte `KourrierDeadEvents`).
- **Négatif** : le débit par tenant est mesuré en e-mails *soumis* : une redélivrance consomme aussi des jetons, ce qui reste négligeable.
- **Négatif** : l'équité est assurée à l'ingestion, pas dans la file d'envoi elle-même (FIFO partagée). Des files par tenant seraient l'étape suivante si un tenant légitime saturait encore l'envoi.
