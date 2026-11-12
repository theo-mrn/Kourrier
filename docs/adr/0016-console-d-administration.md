# ADR 016 : Console d'administration web

**Statut** : Accepté

## Contexte

Le dashboard Grafana ([ADR 005](0005-observabilite-native.md)) montre des **tendances** : débit, latences, profondeur de la file. Il ne répond pas aux questions de l'exploitation au quotidien :

- qu'est devenu l'e-mail de tel utilisateur, et pourquoi a-t-il échoué ?
- que contient la dead-letter queue, et peut-on la rejouer maintenant que la cause est corrigée ?
- quels événements ont été mis de côté ([ADR 015](0015-durcissement-entree-evenementielle.md)), et avec quelle erreur ?
- à quoi ressemble ce template avec ces données ?

Toutes ces informations existent déjà, mais elles demandent `kubectl exec` et la CLI (`kourrier dlq`, `kourrier events`), ou un client HTTP muni d'une clé de tenant. Il faut une interface pour les opérateurs, **optionnelle** : Kourrier doit rester un service sans interface pour qui n'en veut pas.

## Décision

Kourrier embarque une **console web d'administration**, désactivée par défaut (`KOURRIER_CONSOLE_ENABLED=false`).

**Déploiement.** La console est servie par le binaire lui-même, sur un **port dédié** (`:8081`). Elle n'est jamais exposée par le Service de l'API publique. Elle tourne avec l'API (modes `all` et `api`), puisqu'elle a besoin des tenants et des routes.

**Interface.** C'est une SPA React (Vite, TypeScript), compilée puis **embarquée dans le binaire** (`embed`). Elle consomme une API JSON d'administration servie sur le même port (`/api/...`). Il reste un seul artefact à déployer. Node n'intervient qu'au build : un binaire construit sans le front sert une page qui explique comment le construire. Les polices sont embarquées : la console ne charge aucune ressource externe.

**Authentification par jeton administrateur.**

- Le jeton est lu depuis un fichier (`KOURRIER_CONSOLE_TOKEN_FILE`, monté depuis un Secret). Il doit faire au moins 32 caractères. Il est comparé en temps constant.
- Une connexion réussie ouvre une **session sans état** : un cookie `HttpOnly`, `SameSite=Strict` et `Secure`, qui porte une date d'expiration signée par HMAC-SHA256. La clé HMAC est dérivée du jeton : tous les Pods acceptent la même session, et changer le jeton révoque toutes les sessions.
- Protection CSRF : en plus de `SameSite=Strict`, toute requête qui modifie l'état doit porter l'en-tête `X-Kourrier-Console` et une origine identique à l'hôte. Aucune requête cross-origin n'est autorisée (pas de CORS).
- Les en-têtes de sécurité (CSP stricte, `frame-ancestors 'none'`, `nosniff`) s'appliquent à toutes les réponses. La prévisualisation d'un template s'affiche dans une iframe `sandbox`, sous sa propre CSP, sans script.
- Chaque connexion, échec de connexion et action est journalisé avec l'adresse du client.

**Contenu.**

| Vue | Données | Actions |
|-----|---------|---------|
| Tableau de bord | Envois et échecs sur 1 h ou 24 h, taux de succès, délai de livraison, templates les plus utilisés, état de la file | |
| Activité | Fil des derniers changements de statut ; recherche par ID d'e-mail | |
| Dead-letter queue | Jobs en échec, cause, tentatives | Rejouer, supprimer |
| Événements en échec | Entrées de l'ADR 015 | Rejouer (mêmes règles que `kourrier events replay`), supprimer |
| Templates | Templates par tenant et leurs locales | Prévisualiser avec des données |
| Tenants et routes | Expéditeurs autorisés, nombre de clés et leurs scopes, routes et sources | |

Les empreintes des clés d'API ne sont jamais renvoyées.

**Fil d'activité.** Il n'existait pas d'index des e-mails récents : un statut ne se lit que par son ID. Chaque changement de statut (`queued`, `retrying`, `sent`, `failed`) ajoute désormais une entrée à un **stream Redis plafonné** (`{kourrier}:activity`, 10 000 entrées par défaut, `KOURRIER_ACTIVITY_MAX_LEN`, `0` désactive le fil). Une entrée contient l'ID de l'e-mail, le tenant, l'état, le template, les destinataires **masqués** comme dans les logs, la tentative et l'erreur. L'écriture est faite au mieux : un échec est journalisé, il ne fait jamais échouer un envoi. Le fil est indépendant de la console : en mode séparé, ce sont les workers qui l'alimentent. Le tableau de bord en tire ses statistiques : les identifiants d'un stream Redis sont des horodatages, une fenêtre de temps se lit donc directement, sans index supplémentaire. Elles ne couvrent que la période que le fil contient encore.

## Alternatives écartées

- **Rendu côté serveur (templates Go + htmx)** : aucune toolchain Node, mais une interface moins riche (filtres, rafraîchissement, prévisualisation interactive). La SPA est isolée dans `web/console` et n'impose Node qu'au build du front.
- **Authentification déléguée à un proxy (oauth2-proxy, OIDC)** : pas de secret côté Kourrier, mais un composant de plus à déployer et à tester. Le port dédié permet de l'ajouter devant la console sans rien changer à Kourrier.
- **Lecture seule** : surface d'attaque minimale, mais les actions de la CLI restent les plus utiles en incident.

## Conséquences

- **Positif** : l'exploitation courante (suivre un e-mail, vider la DLQ après un incident, vérifier un template) ne demande plus d'accès au cluster. Le binaire reste unique et la console coûte zéro quand elle est désactivée.
- **Négatif** : le build de l'image demande une étape Node. Le jeton administrateur est un secret de plus à gérer, et il donne tous les droits : un seul niveau d'accès pour l'instant. Le fil d'activité ajoute une écriture Redis par changement de statut.
- **Limites** : le fil d'activité ne garde que les dernières entrées (pas d'historique complet). Rejouer un job de la DLQ ne remet pas son statut à `queued` : le statut est mis à jour quand le worker le traite.
