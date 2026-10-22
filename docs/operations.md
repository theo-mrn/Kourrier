# Exploitation

## Dead-letter queue

Les e-mails en échec définitif (template introuvable, adresse rejetée, tentatives épuisées) partent dans la DLQ. Ils ne sont jamais supprimés silencieusement.

```sh
# Lister (dans le Pod : la configuration Redis est déjà dans l'environnement)
kubectl exec deploy/kourrier -- kourrier dlq list -n 50

# Après correction (template ajouté, domaine vérifié…), remettre en file
kubectl exec deploy/kourrier -- kourrier dlq replay -n 50
```

Le rejeu remet le compteur de tentatives à zéro. La métrique `kourrier_queue_depth{state="dead"}` et l'alerte `KourrierDeadLetters` signalent toute nouvelle entrée.

## Ajouter un tenant ou faire tourner une clé

```sh
kourrier keygen        # affiche la clé (à transmettre au client) et son empreinte
```

1. Ajouter l'empreinte dans `api_keys` du tenant (values Helm `tenants` ou Secret existant).
2. `helm upgrade` : le checksum du Secret déclenche un rolling update.
3. Une fois le client migré, retirer l'ancienne empreinte.

## Modifier un template

Déposer le fichier dans le bucket (`aws s3 cp`, console…). Il est pris en compte en moins de `KOURRIER_TEMPLATES_CACHE_TTL` (1 min), sans redéploiement.

## Alertes (PrometheusRule)

| Alerte | Cause probable | Action |
|--------|----------------|--------|
| `KourrierHighFailureRate` | Domaine non vérifié, template cassé, compte SES suspendu. | `kourrier dlq list`, logs `level=ERROR` dans Loki. |
| `KourrierDeadLetters` | Au moins un e-mail en échec définitif. | Idem. |
| `KourrierQueueBacklog` | Débit entrant supérieur au rate limit ou fournisseur lent. | Vérifier `rate_limit_wait_seconds`, augmenter le quota ou la concurrence. |
| `KourrierHighRetryRate` | Throttling ou panne du fournisseur. | Vérifier la page de statut du fournisseur, le quota SES. |

## Garanties

- **Livraison au moins une fois** : un e-mail accepté (202) est envoyé ou finit en DLQ, même en cas de crash de Pod ou de redémarrage de Redis (avec AOF).
- **Doublons** : évités par l'`Idempotency-Key` côté API et par le statut `sent` côté worker. Un doublon reste théoriquement possible si le Pod meurt entre la réponse du fournisseur et l'écriture du statut.
- **Arrêt propre** : sur SIGTERM, l'API cesse d'accepter des requêtes, les envois en cours se terminent et les jobs en attente du rate limiter sont remis en file.
