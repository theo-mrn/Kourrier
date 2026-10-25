# ADR 011 : Validation, catalogue et prévisualisation des templates par l'API

**Statut** : Accepté. Amende l'[ADR 010](0010-format-des-templates.md), qui réservait la lecture des templates aux workers.

## Contexte

Une application cliente ne connaît que le **nom** d'un template et les données qu'elle y injecte : le contenu vit dans le stockage de Kourrier ([ADR 003](0003-stockage-templates-s3.md)). Avec l'ADR 010, l'API ne lisait jamais ce stockage. Conséquences à l'usage :

- un nom erroné (`bienvenu` au lieu de `welcome`) était accepté avec un `202`, puis l'e-mail finissait en dead-letter queue. L'application ne le découvrait qu'en interrogeant son statut ;
- aucun moyen de savoir quels templates existent pour une clé d'API ;
- pour voir le rendu d'un template, il fallait l'envoyer, ce qui ralentit la mise en forme et les tests en local.

## Décision

L'API lit les templates du tenant authentifié, avec le même moteur et le même cache que les workers :

1. **Validation à l'appel** : `POST /v1/emails` vérifie que le template existe (dans la locale demandée ou la locale par défaut) et qu'il compile. Sinon : `422` avec le type `template-not-found` ou `template-invalid`, et rien n'est mis en file.
2. **Catalogue** : `GET /v1/templates` liste les templates du tenant et leurs locales.
3. **Prévisualisation** : `POST /v1/templates/{nom}/preview` rend un template avec des données, **sans rien envoyer**. Elle répond en JSON (objet, HTML, texte, locale utilisée), ou en HTML brut avec `Accept: text/html`, sous une CSP `sandbox`. `make preview` s'appuie dessus en local.

**Le stockage reste une dépendance souple.** Si la validation ne peut pas lire le stockage (panne, timeout de 2 s), la requête est **acceptée** et le worker réessaiera, comme avant. Une panne de S3 ou R2 ne doit jamais empêcher la mise en file des e-mails. La validation coûte une lecture au premier appel, puis rien tant que le cache est valide (`KOURRIER_TEMPLATES_CACHE_TTL`).

Le stockage gagne une opération `List(prefix)`. Sur S3, elle demande le droit `s3:ListBucket` sur le bucket. Sur R2, un token « Object Read only » le permet.

## Limite connue

La validation porte sur l'existence et la syntaxe, **pas sur les données** : une variable absente des `data` donne `<no value>` dans l'objet et le texte, et une chaîne vide dans le HTML (comportement par défaut des templates Go). La prévisualisation permet de le voir avant l'envoi.

## Conséquences

- **Positif** : les erreurs de nom ou de syntaxe sont visibles immédiatement, à l'appel. Les templates disponibles se découvrent par l'API. Le rendu se vérifie sans envoyer d'e-mail, en local comme en production.
- **Négatif** : l'API dépend désormais du stockage pour valider, mais seulement en lecture, avec cache, et en laissant passer les requêtes en cas de panne. Un template supprimé entre la validation et l'envoi (fenêtre du cache) finit toujours en dead-letter queue.
