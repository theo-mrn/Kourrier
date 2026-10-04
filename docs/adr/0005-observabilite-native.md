# ADR 005 : Observabilité native et dashboarding (Prometheus, Loki & Grafana)

**Statut** : Accepté

## Contexte

Développer et maintenir un dashboard web interne alourdit le code, complexifie la sécurité et duplique les outils d'observabilité existants.

## Décision

Pas de dashboard intégré. L'observabilité est 100 % native :

- **Métriques Prometheus** sur `/metrics`, servies sur un **port d'administration séparé** (`:9090`, avec `/livez` et `/readyz`) qui n'est jamais exposé par l'Ingress.
- **Logs JSON structurés** sur stdout (`log/slog`), ingérés par Loki. Chaque ligne porte `email_id`, `tenant` et `request_id`.
- **Dashboard Grafana** packagé dans le Chart Helm : ConfigMap portant le label `grafana_dashboard: "1"`, découverte par le sidecar Grafana.
- **Règles d'alerte** (`PrometheusRule`, optionnelles) : taux d'échec, dead-letters, backlog, retries.

Règles de conception :

- **Cardinalité bornée** : les labels Prometheus se limitent au tenant (issu de la configuration), au provider, à la route HTTP (le pattern, jamais le chemin brut) et à l'état. Jamais de destinataire, de template libre ou d'ID.
- **Données personnelles (RGPD)** : les adresses e-mail sont masquées dans les logs (`j***@example.com`). Le domaine est conservé pour diagnostiquer la délivrabilité.

## Conséquences

- **Positif** : zéro code frontend, intégration immédiate dans les écosystèmes Kubernetes existants.
- **Négatif** : l'infrastructure doit disposer de Prometheus, de Loki et du sidecar Grafana (standards sur les clusters modernes). Le ServiceMonitor et la PrometheusRule exigent le Prometheus Operator.
