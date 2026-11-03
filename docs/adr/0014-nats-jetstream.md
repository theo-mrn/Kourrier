# ADR 014 : NATS JetStream comme premier broker

**Statut** : Accepté

## Contexte

L'entrée HTTP ([ADR 012](0012-entree-evenementielle-cloudevents.md)) suppose un intermédiaire capable de pousser les événements (Knative, Dapr…). Beaucoup d'équipes veulent que Kourrier **consomme directement** leur bus. Il faut choisir le premier broker supporté en mode pull, sans fermer la porte aux autres.

| Broker | Avis |
|--------|------|
| Kafka | Le plus répandu en entreprise, mais lourd à opérer pour la cible (équipes petites et moyennes sur Kubernetes) et sans dépendance Go légère et officielle. |
| RabbitMQ | Répandu, mais modèle AMQP plus éloigné des logs persistants et du rejeu. |
| Redis Streams | Déjà présent, mais mélanger la file d'e-mails et le bus métier dans le même Redis couple les deux et fausse la backpressure. |
| **NATS JetStream** | Projet CNCF (incubating), binaire unique et léger, client Go officiel, consommateurs durables avec acquittement explicite, `Term` et `NakWithDelay`. Debezium Server sait y publier un outbox. |

## Décision

Le premier transport pull est **NATS JetStream** (`github.com/nats-io/nats.go/jetstream`).

- Une **source** lie un consommateur durable (`stream`, `consumer`, `filter_subjects`) à **un tenant**. Elle est déclarée dans le fichier de routes, sous `sources.nats`.
- Kourrier **crée ou met à jour** le consommateur au démarrage, mais **ne crée jamais de stream** : le stream appartient aux producteurs ou à l'équipe plate-forme. Avec `bind: true`, Kourrier se contente de se lier à un consommateur géré ailleurs (NACK, Terraform), ce qui permet de lui retirer le droit de créer des consommateurs.
- Paramètres par défaut : `AckExplicit`, `ack_wait` de 30 s (15 s minimum, soit plus que les 10 s qu'un événement peut prendre à traiter), `max_ack_pending` de 256 (contrôle de flux) et `max_deliver` illimité. Une erreur transitoire ne doit pas perdre d'événement ; les messages empoisonnés sont terminés explicitement (`Term`, qui publie un advisory `MSG_TERMINATED`).
- Un délai de redélivrance exponentiel est appliqué sur erreur transitoire (1 s, 2 s, 4 s… jusqu'à 1 min), et fixé à 30 s en cas de backpressure.
- La connexion se fait en tâche de fond avec reconnexion infinie : **une panne de NATS n'empêche jamais l'API HTTP de démarrer** ni le Pod d'être prêt. L'état est exposé par `kourrier_event_source_up` et une alerte.
- Les identifiants se lisent depuis des fichiers montés (creds JWT ou seed NKey, CA, certificat client pour le mTLS), jamais depuis des variables d'environnement.
- À l'arrêt, les messages déjà reçus sont traités et acquittés (`Drain`) avant la fermeture.

Les autres brokers s'ajouteront en implémentant le même adaptateur (`internal/source/<broker>`) au-dessus du dispatcher, sans toucher au routage.

## Conséquences

- **Positif** : l'intégration est légère, l'acquittement fin et la reprise sans perte ; les permissions nécessaires sont minimales et documentées.
- **Négatif** : les utilisateurs de Kafka passent pour l'instant par un pont (Knative KafkaSource vers le sink HTTP, ou Debezium Server).
- **Négatif** : le traitement est séquentiel par source et par Pod (voir les goulets d'étranglement dans [events.md](../events.md#limites-connues)) ; le débit se gagne en ajoutant des réplicas.
