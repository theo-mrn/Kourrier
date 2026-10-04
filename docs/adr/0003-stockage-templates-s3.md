# ADR 003 : Gestion et stockage des templates (stockage S3 / MinIO)

**Statut** : Accepté

## Contexte

Les templates d'e-mails ne doivent pas être figés dans le binaire ou dans le Chart Helm, afin de conserver un outil générique et open-source.

## Décision

Les templates sont stockés sur un object storage compatible S3 et manipulés via l'interface Go `storage.Provider` (`Get`, `Ping`). Deux implémentations :

- `s3` : AWS S3 ou tout service compatible (MinIO, RustFS, SeaweedFS, Garage…) via `KOURRIER_STORAGE_S3_ENDPOINT` et `KOURRIER_STORAGE_S3_USE_PATH_STYLE=true`. Sur AWS, les identifiants viennent de la chaîne par défaut (IRSA / EKS Pod Identity).
- `filesystem` : un répertoire local, pour le développement ou un volume monté. L'accès passe par `os.Root`, ce qui interdit toute sortie du répertoire.

Le format des templates est détaillé dans l'[ADR 010](0010-format-des-templates.md).

## Note (octobre 2026)

MinIO ne publie plus d'images Docker communautaires (dépôts Docker Hub et quay.io inaccessibles). Le service ne dépend que de l'API S3, ce qui n'a aucun impact sur le code. L'environnement de développement utilise **RustFS** (Apache 2.0). En production, n'importe quel service S3 convient.

## Conséquences

- **Positif** : dissociation totale entre le moteur et le contenu. Les templates se modifient à chaud dans le bucket, sans redéploiement (cache avec TTL).
- **Négatif** : le rendu nécessite un accès réseau au stockage. Une indisponibilité de S3 provoque des retries (erreur transitoire), jamais une perte d'e-mail.
