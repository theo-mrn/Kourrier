# Kourrier

**La couche e-mail de votre plate-forme Kubernetes** : asynchrone, cloud-native, auto-hébergée et agnostique du fournisseur, écrite en Go.

Vos services publient des **événements de domaine** (`user.signed_up`, `order.paid`) au format [CloudEvents](https://cloudevents.io). Kourrier les consomme et en déduit les e-mails à envoyer grâce à des **routes déclaratives** versionnées dans Git. Le code métier ne connaît plus l'e-mail ; ajouter une notification revient à ajouter une route. Pour les envois directs, l'API HTTP reste disponible.

```
Événements (NATS JetStream, POST /v1/events) ──► routes CEL ─┐
                                                              ├─► Redis Streams ──► Workers ──► SES / SMTP
API (POST /v1/emails) ────────────────────────────────────────┘                        │
                                                                              templates S3 (cache)
```

```yaml
# routes.yaml
routes:
  - name: welcome
    tenant: yner
    match:
      types: [com.yner.user.signed_up]
      when: data.email_verified
    email:
      template: welcome
      to: data.email
      locale: data.?locale.orValue("fr")
```

## Fonctionnalités

- **Piloté par les événements** : consommateur NATS JetStream (pull, ack explicite) et sink HTTP CloudEvents. Les routes [CEL](https://cel.dev) (le langage de Kubernetes) sont compilées au démarrage, avec un coût borné, et testables en CI (`kourrier routes test`). Compatible avec le pattern *outbox*. Aucun événement n'est perdu en silence : les échecs sont conservés et rejouables (`kourrier events replay`). Voir [docs/events.md](docs/events.md).
- **Asynchrone et fiable** : Redis Streams avec consumer groups, livraison au moins une fois, reprise des jobs d'un Pod mort, retries avec backoff exponentiel, dead-letter queue rejouable.
- **Agnostique** : drivers `aws-ses`, `smtp`, `console`, `filesystem` derrière une interface unique. Le stockage des templates accepte S3, MinIO, RustFS ou un dossier local.
- **Sûr par défaut** : le driver par défaut n'envoie rien. Clés API par tenant avec scopes (`emails`, `events`), dont seules les empreintes SHA-256 sont stockées ; expéditeurs autorisés, limites d'e-mails par tenant, en-têtes filtrés, échappement HTML automatique.
- **Idempotent** : l'en-tête `Idempotency-Key` côté API et la clé `route + source + id` côté événements évitent les doublons lors des renvois et redélivrances.
- **Rate limiting distribué** (GCRA sur Redis) : le quota SES est respecté quel que soit le nombre de replicas.
- **Observabilité native** : métriques Prometheus, logs JSON (adresses masquées), dashboard Grafana et alertes livrés dans le Chart Helm.
- **Léger** : un binaire statique (environ 40 Mo), image distroless non-root d'environ 53 Mo.

## Tester en local

Aucun compte AWS, aucune connexion Internet : en local, Kourrier **affiche les e-mails dans la console** au lieu de les envoyer (driver `console`, [ADR 004](docs/adr/0004-abstraction-fournisseur.md)).

```sh
make up      # Redis, NATS et Kourrier dans Docker (API sur http://localhost:8180)
make send    # envoie l'e-mail de démonstration « welcome » via l'API
make event   # publie un CloudEvent sur NATS, routé vers le même e-mail
make logs    # affiche l'e-mail rendu (texte et HTML)
make down    # arrête tout
```

Ou bien, pour travailler sur le code Go :

```sh
make run     # lance Kourrier depuis les sources, avec son propre Redis ; les e-mails
             # s'affichent dans ce terminal
make send    # depuis un autre terminal
```

Les templates sont relus à chaque envoi depuis `examples/templates/` : modifiez un fichier, refaites `make send`, le résultat s'affiche aussitôt. Pour la mise en page, `make preview` rend le template sans l'envoyer et l'ouvre dans le navigateur (`make preview TEMPLATE=welcome LOCALE=en`).

| Mode | Templates | Livraison | Pour quoi |
|------|-----------|-----------|-----------|
| `make up` / `make run` | dossier local | console | développer, mettre en forme les templates |
| `make up-full` | S3 (RustFS) | SMTP vers Mailpit, <http://localhost:8125> | tester les drivers de production |

Les ports par défaut (8180, 9190, 6390, 8125, 9200) évitent les ports usuels : le stack tourne à côté d'autres projets. Ils restent surchargeables, par exemple `KOURRIER_PORT=8181 make up`.

## Envoyer un e-mail

```sh
curl -X POST http://localhost:8180/v1/emails \
  -H "Authorization: Bearer kr_dev_local_insecure_key" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: 6f1c2a7e-signup-42" \
  -d '{
        "to": [{ "email": "jane@example.com", "name": "Jane" }],
        "template": {
          "name": "welcome",
          "locale": "fr",
          "data": { "name": "Jane", "login_url": "https://example.com/login" }
        }
      }'
# {"id":"0192f1b4-…","status":"queued"}

curl -H "Authorization: Bearer kr_dev_local_insecure_key" http://localhost:8180/v1/emails/0192f1b4-…
# {"id":"0192f1b4-…","status":"sent","attempts":1,…}
```

Votre application n'envoie que le **nom** du template et ses données : le contenu vit dans le stockage de Kourrier. Pour savoir ce qui existe et vérifier un rendu :

```sh
curl -H "Authorization: Bearer kr_dev_local_insecure_key" http://localhost:8180/v1/templates
# {"templates":[{"name":"welcome","locales":["en","fr"]}]}

curl -X POST http://localhost:8180/v1/templates/welcome/preview \
  -H "Authorization: Bearer kr_dev_local_insecure_key" \
  -d '{"locale":"fr","data":{"name":"Jane"}}'
# {"locale":"fr","subject":"Bienvenue sur Kourrier, Jane !","html":"…","text":"…"}
```

Un envoi avec un template inconnu est refusé immédiatement (`422 template-not-found`), il n'est jamais mis en file.

La référence complète se trouve dans [docs/api.md](docs/api.md).

## Templates

Organisation dans le bucket :

```
<tenant>/_layout.html                       layout HTML partagé (optionnel)
<tenant>/<template>/<locale>/subject.txt
<tenant>/<template>/<locale>/body.html      html/template
<tenant>/<template>/<locale>/body.txt       text/template
```

Un exemple complet se trouve dans [examples/templates](examples/templates/demo). Les règles détaillées sont dans l'[ADR 010](docs/adr/0010-format-des-templates.md).

## Déploiement sur Kubernetes

```sh
kourrier keygen   # génère une clé API et son empreinte pour tenants.yaml

helm install kourrier deploy/helm/kourrier \
  --set mailer.provider=aws-ses \
  --set mailer.ses.region=eu-west-3 \
  --set redis.addr=redis-master:6379 \
  --set storage.s3.bucket=my-templates \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/kourrier \
  --set serviceMonitor.enabled=true \
  --set prometheusRule.enabled=true \
  -f my-tenants.yaml
```

Le chart fournit :
- un Deployment durci (non-root, rootfs en lecture seule, seccomp) ;
- des probes et un arrêt propre ;
- un Secret pour les tenants, avec checksum pour redémarrer les Pods à chaque modification ;
- les ressources optionnelles suivantes : Ingress, HPA, PDB, NetworkPolicy, ServiceMonitor et PrometheusRule ;
- le **dashboard Grafana** (ConfigMap portant le label `grafana_dashboard`).

Toutes les options sont décrites dans [values.yaml](deploy/helm/kourrier/values.yaml) et [docs/configuration.md](docs/configuration.md).

## Documentation

- [Événements et routes](docs/events.md)
- [Architecture](docs/architecture.md)
- [Décisions d'architecture (ADRs)](docs/adr/README.md)
- [API](docs/api.md)
- [Configuration](docs/configuration.md)
- [Exploitation (DLQ, rotation des clés, alertes)](docs/operations.md)

## Développement

```sh
make test        # tests avec le détecteur de race (Redis simulé par miniredis)
make lint        # go vet et golangci-lint
make build       # bin/kourrier
make helm-lint   # lint et rendu du chart
make help        # toutes les cibles
```

```
cmd/kourrier/          point d'entrée et CLI (serve, keygen, dlq, routes, healthcheck)
internal/app/          racine de composition et cycle de vie
internal/api/          API HTTP (/v1/emails, /v1/events) et serveur d'administration
internal/submit/       service de soumission commun à toutes les entrées
internal/cloudevent/   parseur CloudEvents strict
internal/routing/      fichier de routes et expressions CEL
internal/dispatch/     événement → e-mails, idempotence, backpressure
internal/source/natsjs/ consommateur NATS JetStream
internal/queue/        file Redis Streams
internal/worker/       pool de workers
internal/templates/    rendu et cache des templates
internal/provider/     drivers d'envoi (ses, smtp, console, filesystem, memory)
internal/storage/      stockage des templates (s3store, fsstore)
deploy/helm/kourrier/  Chart Helm et dashboard Grafana
docs/                  architecture, ADRs, API, configuration, exploitation
```
