# ADR 004 : Moteur d'envoi et abstraction du fournisseur (interface EmailProvider et drivers de test)

**Statut** : Accepté (mis à jour)

## Contexte

Le service doit garantir une excellente délivrabilité en production (AWS SES). Les développeurs doivent pouvoir travailler en local, hors ligne, sans risque d'envoyer de faux e-mails à de vrais utilisateurs. Il faut aussi garder la liberté de basculer vers d'autres fournisseurs (SMTP, Resend, etc.).

## Décision

Une interface Go stricte, `provider.Provider` :

```go
type Provider interface {
    Name() string
    Send(ctx context.Context, msg *mail.Message) (Result, error)
}
```

Elle est déclinée en drivers interchangeables, sélectionnés par `KOURRIER_MAILER_PROVIDER` :

| Driver | Usage |
|--------|-------|
| `aws-ses` | Production, SDK officiel AWS SES v2 (configuration set optionnel pour les événements bounce/complaint). |
| `smtp` | Relais SMTP classique, ou faux serveur local (Mailpit / MailHog). |
| `console` | **Défaut.** Affiche l'e-mail en clair dans le terminal. |
| `filesystem` | Écrit `.html`, `.txt` et `.json` dans un dossier, pour une vérification visuelle dans un navigateur. |
| `memory` | Tests automatisés uniquement (non sélectionnable par configuration). |

Chaque driver **classe ses erreurs** : une erreur enveloppée par `retry.Permanent` (adresse rejetée, domaine non vérifié, réponse SMTP 5xx) part directement en dead-letter queue. Toute autre erreur (throttling, réseau, SMTP 4xx) est retentée avec backoff.

Le driver par défaut est `console`. Une configuration incomplète ne peut donc jamais envoyer de vrais e-mails (*fail-safe*).

## Récapitulatif par environnement

```sh
# En production sur le cluster Kubernetes :
KOURRIER_MAILER_PROVIDER=aws-ses

# En local sur la machine du développeur :
KOURRIER_MAILER_PROVIDER=console   # ou smtp pointant vers Mailpit
```

Toutes les variables portent le préfixe `KOURRIER_` (la variable `MAILER_PROVIDER` des ADRs initiaux devient `KOURRIER_MAILER_PROVIDER`).

## Conséquences

- **Positif** : aucune friction en développement, aucun compte AWS ni connexion Internet nécessaire, aucun envoi accidentel possible. Ajouter un fournisseur (Resend, Postmark…) revient à écrire un package de quelques dizaines de lignes.
- **Négatif** : légère surcharge de code pour maintenir plusieurs drivers, largement compensée par le confort de développement.
