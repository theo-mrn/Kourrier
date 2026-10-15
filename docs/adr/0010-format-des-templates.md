# ADR 010 : Format, localisation et cache des templates

**Statut** : Accepté

## Contexte

L'[ADR 003](0003-stockage-templates-s3.md) place les templates dans un bucket. Il reste à fixer leur organisation, leur langage, la gestion des langues et le compromis entre fraîcheur et performance.

## Décision

**Organisation du bucket :**

```
<tenant>/_layout.html                 optionnel, layout HTML partagé
<tenant>/<template>/<locale>/subject.txt   obligatoire
<tenant>/<template>/<locale>/body.html     HTML (html et/ou txt obligatoire)
<tenant>/<template>/<locale>/body.txt      texte brut (fortement recommandé)
```

- **Langage** : `html/template` de la bibliothèque standard pour le HTML (échappement contextuel automatique : les données client ne peuvent pas injecter de HTML) et `text/template` pour l'objet et le texte. Les helpers sont limités au formatage : `default`, `upper`, `lower`, `trim`, `year`, `date`. Il n'y a volontairement aucun `safeHTML`.
- **Layout** : `_layout.html` est chargé dans le même jeu de templates. Un corps l'utilise avec `{{template "layout" .}}` et définit les blocs `content`, `title`, etc.
- **Localisation** : la locale demandée (`fr`, `en-GB`…) est essayée en premier, puis `KOURRIER_TEMPLATES_DEFAULT_LOCALE`.
- **Sécurité** : noms de tenant et de template limités à `[a-z0-9][a-z0-9_-]{0,63}` (pas de path traversal). L'objet rendu est replié sur une seule ligne (anti-injection d'en-têtes).
- **Cache** : les templates compilés sont gardés en mémoire pendant `KOURRIER_TEMPLATES_CACHE_TTL` (1 min). Les chargements concurrents sont fusionnés (*singleflight*). Une modification dans le bucket est donc visible en moins d'une minute, sans redéploiement.
- **Erreurs** : template introuvable, erreur de syntaxe ou d'exécution sont **permanentes** (DLQ). Une indisponibilité du stockage est **transitoire** (retry).

Le rendu se fait dans le worker, pas dans l'API : l'API reste indépendante de S3 et répond en quelques millisecondes.

## Conséquences

- **Positif** : aucun moteur tiers, sécurité par défaut, templates modifiables à chaud.
- **Négatif** : un template invalide n'est détecté qu'au moment de l'envoi (en DLQ). Une commande de validation (`kourrier templates lint`) est une évolution naturelle.
