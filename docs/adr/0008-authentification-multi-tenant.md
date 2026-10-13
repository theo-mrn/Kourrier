# ADR 008 : Authentification et isolation multi-tenant

**Statut** : Accepté

## Contexte

Sans authentification, n'importe quel Pod du cluster peut envoyer des e-mails au nom de n'importe quel domaine vérifié. Le service deviendrait un relais ouvert, avec un risque direct pour la réputation d'envoi (suspension du compte SES).

## Décision

Chaque application cliente est un **tenant** déclaré dans un fichier `tenants.yaml`, monté depuis un Secret Kubernetes :

```yaml
tenants:
  - id: yner
    api_keys:
      - sha256: <empreinte>            # plusieurs clés possibles : rotation sans coupure
    default_sender: { email: no-reply@yner.fr, name: Yner }
    allowed_senders: ["no-reply@yner.fr", "*@notifications.yner.fr"]
```

- **Clés API** au format `kr_<43 caractères>`, générées par `kourrier keygen`. Seule leur empreinte SHA-256 est stockée. Le préfixe `kr_` facilite la détection par les scanners de secrets. Authentification : `Authorization: Bearer <clé>`.
- **Expéditeurs autorisés** : un tenant ne peut utiliser qu'une adresse listée exactement ou un domaine joker (`*@domaine`). Sinon, `403`.
- **Espace de noms des templates** : un tenant ne voit que `<tenant-id>/…` dans le bucket.
- **Isolation des statuts** : `GET /v1/emails/{id}` d'un autre tenant renvoie `404`, ce qui ne révèle pas l'existence de l'ID.
- **En-têtes restreints** : seuls `X-*` et `List-Unsubscribe`, `List-Unsubscribe-Post`, `List-Id` sont acceptés, sans retour à la ligne (anti-injection d'en-têtes).
- La configuration est validée au démarrage (ID, empreintes, clés en double, expéditeur par défaut autorisé). Une erreur empêche le démarrage.

En complément, la `NetworkPolicy` du chart restreint l'accès au port de l'API.

## Conséquences

- **Positif** : aucun relais ouvert, rotation des clés sans coupure, aucune clé en clair dans le cluster.
- **Négatif** : l'ajout d'un tenant nécessite un redémarrage, automatique avec Helm grâce au checksum du Secret. Un rechargement à chaud pourra venir plus tard si le besoin apparaît.
