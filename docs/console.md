# Console d'administration

Une interface web pour l'exploitation courante, sans `kubectl exec` ni clé de tenant : suivre un e-mail, vider la dead-letter queue après un incident, rejouer des événements en échec, vérifier le rendu d'un template. Décision : [ADR 016](adr/0016-console-d-administration.md).

La console est **désactivée par défaut**. Elle est embarquée dans le binaire et servie par Kourrier lui-même, sur un port dédié (`:8081`), avec l'API (modes `all` et `api`).

## Ce qu'elle montre

| Page | Contenu | Actions |
|------|---------|---------|
| Overview | File (prêts, en cours, en attente de retry, DLQ), événements en échec, configuration de l'instance. Rafraîchie toutes les 5 s. | |
| Activity | Fil des derniers changements de statut, filtrable par tenant et par état ; recherche par ID ; chronologie d'un e-mail. | |
| Dead letters | Jobs en échec définitif : cause, tentatives, destinataires masqués. | Rejouer, supprimer |
| Failed events | Événements conservés par l'entrée événementielle ([ADR 015](adr/0015-durcissement-entree-evenementielle.md)), avec le CloudEvent complet. | Rejouer, supprimer |
| Templates | Templates de chaque tenant et leurs locales. | Prévisualiser avec des données |
| Tenants | Expéditeurs autorisés, clés et leurs scopes. Les empreintes des clés ne sont jamais renvoyées. | |
| Routes | Routes (types, condition CEL, template) et sources NATS. | |

Rejouer depuis la console suit les mêmes règles que la CLI : un job de la DLQ repart avec un compteur de tentatives à zéro ; un événement ne rejoue que les routes qui avaient échoué, et un nouvel échec le conserve sous une nouvelle entrée (`kourrier events replay`). **Corrigez la cause avant de rejouer**, sinon les e-mails échoueront de nouveau.

## Activer la console

### En local

`make up` (et `make run`) l'activent sur <http://localhost:8181>, avec le jeton de développement de [dev/console-token](../dev/console-token).

Pour travailler sur l'interface avec rechargement à chaud (Node 24 et pnpm) :

```sh
make up            # Kourrier, dont l'API d'administration sur :8181
make console-dev   # Vite sur http://localhost:5173, qui relaie /api vers :8181
```

### Sur Kubernetes

1. Créer le jeton, d'au moins 32 caractères :

   ```sh
   kubectl -n kourrier create secret generic kourrier-console \
     --from-literal=token="$(openssl rand -base64 32)"
   ```

2. Activer la console dans le chart :

   ```yaml
   console:
     enabled: true
     tokenSecret:
       name: kourrier-console
       key: token
   ```

3. Y accéder. Le chart crée un Service dédié (`<release>-console`), que le Service et l'Ingress de l'API publique n'exposent jamais :

   ```sh
   kubectl -n kourrier port-forward svc/kourrier-console 8081
   # http://localhost:8081
   ```

   Pour un accès permanent, placez devant ce Service un Ingress à vous, **en HTTPS**, idéalement restreint (VPN, liste d'adresses ou proxy d'authentification comme oauth2-proxy).

Variables : [configuration.md](configuration.md#console-dadministration).

## Sécurité

- **Jeton administrateur** : lu depuis un fichier, comparé en temps constant, il donne tous les droits de la console. Traitez-le comme un mot de passe d'administration.
- **Sessions** : cookie `HttpOnly`, `SameSite=Strict` et `Secure`, portant une date d'expiration signée (HMAC-SHA256), valable 12 h par défaut (`KOURRIER_CONSOLE_SESSION_TTL`). Toutes les répliques acceptent la même session. **Changer le jeton révoque toutes les sessions.**
- **CSRF** : toute écriture exige l'en-tête `X-Kourrier-Console` et une origine identique ; aucune requête cross-origin n'est acceptée.
- **En-têtes** : CSP stricte (aucune ressource externe, aucun script inline), `frame-ancestors 'none'`, `nosniff`. La prévisualisation d'un template s'affiche dans une iframe `sandbox`, sans script.
- **Données** : les destinataires sont masqués partout (`j***@example.com`). Le contenu des événements en échec, lui, est affiché tel quel : c'est ce qu'il faut pour diagnostiquer.
- **Journal** : chaque connexion, échec de connexion et action (rejeu, suppression) est journalisé avec l'adresse du client (`msg="console: ..."`).

## Fil d'activité

La liste des e-mails récents repose sur un stream Redis plafonné (`{kourrier}:activity`, 10 000 entrées par défaut) alimenté à chaque changement de statut. L'entrée `queued` est écrite atomiquement avec le job ; les autres le sont au mieux, sans jamais faire échouer un envoi. Le fil coûte une écriture Redis par changement de statut ; `KOURRIER_ACTIVITY_MAX_LEN=0` le désactive.

Ce n'est pas un historique complet : seules les dernières entrées sont gardées, et un statut expire après `KOURRIER_STATUS_TTL`.

## Rotation du jeton

1. Mettre à jour le Secret (`kubectl create secret ... --dry-run=client -o yaml | kubectl apply -f -`).
2. Redémarrer les Pods (`kubectl rollout restart deploy/kourrier`) : le jeton est lu au démarrage.

Les sessions ouvertes avec l'ancien jeton sont alors refusées.

## Développement de l'interface

Le code est dans [web/console](../web/console) : Vite, React 19, TypeScript strict, TanStack Query, Tailwind CSS. Le build est écrit dans `internal/console/dist`, embarqué par `go:embed`. Un binaire construit sans lui sert une page qui explique comment le construire ; l'image Docker l'inclut toujours.

```sh
make console-check   # format, lint, types, tests (Vitest)
make console-build   # puis make build
```

pnpm n'installe que des versions publiées depuis au moins 14 jours (`minimumReleaseAge`), pour limiter l'exposition aux paquets compromis.
