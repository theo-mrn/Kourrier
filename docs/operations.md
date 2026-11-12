# Exploitation

## Dead-letter queue

Les e-mails en échec définitif (template introuvable, adresse rejetée, tentatives épuisées) partent dans la DLQ. Ils ne sont jamais supprimés silencieusement.

```sh
# Lister (dans le Pod : la configuration Redis est déjà dans l'environnement)
kubectl exec deploy/kourrier -- kourrier dlq list -n 50

# Après correction (template ajouté, domaine vérifié…), remettre en file
kubectl exec deploy/kourrier -- kourrier dlq replay -n 50
```

Le rejeu remet le compteur de tentatives à zéro. La [console d'administration](console.md), si elle est activée, permet les mêmes opérations entrée par entrée (consulter, rejouer, supprimer), ainsi que pour les événements en échec. La métrique `kourrier_queue_depth{state="dead"}` et l'alerte `KourrierDeadLetters` signalent toute nouvelle entrée.

## Ajouter un tenant ou faire tourner une clé

```sh
kourrier keygen                   # clé avec tous les scopes (emails, events)
kourrier keygen -scopes events    # clé pour un producteur d'événements uniquement
```

1. Ajouter l'empreinte dans `api_keys` du tenant (values Helm `tenants` ou Secret existant).
2. `helm upgrade` : le checksum du Secret déclenche un rolling update.
3. Une fois le client migré, retirer l'ancienne empreinte.

## Modifier un template

Déposer le fichier dans le bucket (`aws s3 cp`, console…). Il est pris en compte en moins de `KOURRIER_TEMPLATES_CACHE_TTL` (1 min), sans redéploiement.

## Événements

Les routes sont du code : modifiez-les par PR et validez-les en CI.

```sh
kourrier routes check -tenants tenants.yaml routes.yaml
kourrier routes test -tenants tenants.yaml -tenant shop -event fixtures/order-paid.json routes.yaml
```

Un changement du fichier (values `events.routes` ou ConfigMap existante gérée par checksum) redémarre les Pods progressivement. Un fichier invalide empêche le nouveau Pod de démarrer : le rolling update s'arrête (`maxUnavailable: 0`) et les anciens Pods continuent de servir.

- **Événements rejetés** : le log `event rejected` donne la raison par route. Côté NATS, capturez les advisories `MSG_TERMINATED` pour conserver les événements terminés.
- **Source arrêtée** : `kourrier_event_source_up == 0`. Vérifiez la connexion, l'existence du stream et du consommateur, et les permissions (`cannot start consuming` dans les logs).
- **Backpressure** : `kourrier_events_backpressure == 1`. Les e-mails partent moins vite qu'ils n'arrivent ; l'arriéré reste dans NATS. Agissez sur l'envoi (quota fournisseur, rate limit, workers), pas sur l'ingestion.
- **Événements en échec** : `kourrier events list`, puis `kourrier events replay` une fois la cause corrigée (voir [events.md](events.md#événements-en-échec)). L'alerte `KourrierDeadEvents` signale toute entrée.
- **Rejouer des événements depuis le broker** : créez un consommateur temporaire avec la politique de livraison voulue. Dans le TTL d'idempotence, les e-mails déjà créés ne sont pas renvoyés.

## Alertes (PrometheusRule)

| Alerte | Cause probable | Action |
|--------|----------------|--------|
| `KourrierHighFailureRate` | Domaine non vérifié, template cassé, compte SES suspendu. | `kourrier dlq list`, logs `level=ERROR` dans Loki. |
| `KourrierDeadLetters` | Au moins un e-mail en échec définitif. | Idem. |
| `KourrierQueueBacklog` | Débit entrant supérieur au rate limit ou fournisseur lent. | Vérifier `rate_limit_wait_seconds`, augmenter le quota ou la concurrence. |
| `KourrierHighRetryRate` | Throttling ou panne du fournisseur. | Vérifier la page de statut du fournisseur, le quota SES. |
| `KourrierEventsRejected` | Producteur qui envoie des événements invalides, ou route inévaluable (champ manquant). | Log `event rejected`, puis corriger la route (`?`/`orValue`) ou le producteur. |
| `KourrierDeadEvents` | Événements conservés après un échec (route, template, producteur). | `kourrier events list`, corriger, `kourrier events replay`. |
| `KourrierEventSourceDown` | NATS injoignable, stream ou consommateur absent, permissions. | Logs `cannot start consuming`, `nats consumer info`. |
| `KourrierEventsBackpressure` | Envoi plus lent que la production d'événements. | Voir `KourrierQueueBacklog`. |

## Garanties

- **Livraison au moins une fois** : un e-mail accepté (202) est envoyé ou finit en DLQ, même en cas de crash de Pod ou de redémarrage de Redis (avec AOF).
- **Doublons** : évités par l'`Idempotency-Key` côté API, par la clé `route + source + id` côté événements (pendant `KOURRIER_IDEMPOTENCY_TTL`) et par le statut `sent` côté worker. Un doublon reste théoriquement possible si le Pod meurt entre la réponse du fournisseur et l'écriture du statut.
- **Arrêt propre** : sur SIGTERM, l'API cesse d'accepter des requêtes, les envois en cours se terminent et les jobs en attente du rate limiter sont remis en file.
