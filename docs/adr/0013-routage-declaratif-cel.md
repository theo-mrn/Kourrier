# ADR 013 : Routage déclaratif avec CEL

**Statut** : Accepté

## Contexte

Une route doit sélectionner des événements, puis en extraire les destinataires, la locale et les données du template. Cette logique est écrite par des équipes produit ou plate-forme, versionnée dans Git et exécutée sur des données qui viennent d'autres systèmes. Elle doit donc être :

- expressive, pour accéder à des champs imbriqués, combiner, filtrer et transformer des listes ;
- **sûre** : pas d'I/O, pas de boucle infinie, coût borné ;
- vérifiable **avant** le déploiement, en CI ;
- familière à l'écosystème cloud-native.

Options étudiées :

| Option | Avis |
|--------|------|
| `text/template` Go | Turing-complet, erreurs à l'exécution, pas de types. |
| JSONPath / JMESPath | Extraction seulement, pas de prédicats riches ni de construction d'objets. |
| Lua / JavaScript embarqué | Puissant mais non borné : sandboxing difficile, surface d'attaque large. |
| **CEL** (Common Expression Language) | Conçu pour cet usage. Utilisé par Kubernetes (`ValidatingAdmissionPolicy`, validation des CRD), Envoy, Istio et Google Cloud IAM. Non Turing-complet, typé, avec un coût calculable. |

## Décision

Les routes utilisent **CEL** (`cel.dev/cel-go`).

- **Variables** : `event` (attributs de contexte et extensions, `map(string, dyn)`) et `data` (charge utile JSON décodée, `dyn`).
- **Bibliothèques** : standard, extension `strings`, types optionnels (`data.?locale.orValue("fr")`). Aucune fonction n'accède au réseau, aux fichiers ou à l'horloge.
- **Compilation au démarrage** : une expression invalide, ou dont le type de retour est incompatible (`when` doit renvoyer un booléen), **empêche le démarrage**. `kourrier routes check` fait la même vérification hors ligne.
- **Bornes** : 4 Kio par expression, un coût d'évaluation de 100 000 unités maximum (une compréhension imbriquée sur une grande liste est interrompue) et une profondeur de 32 niveaux pour les valeurs produites.
- **Échec explicite** : un champ manquant fait échouer la route (`rejected`) au lieu de produire un e-mail incomplet. Les champs facultatifs s'écrivent avec les types optionnels.
- Les entiers JSON sont décodés en `int64` quand ils tiennent, pour ne pas arrondir les identifiants supérieurs à 2^53.

Le schéma du fichier est versionné (`apiVersion: kourrier/v1alpha1`) et les champs inconnus sont refusés : une faute de frappe (`tempalte`) est détectée au lieu d'être ignorée.

## Conséquences

- **Positif** : les routes sont du code relu et testé (`kourrier routes test` affiche l'e-mail produit par un événement d'exemple). Une erreur de syntaxe ne peut pas atteindre la production.
- **Positif** : le format est proche de ce que les équipes plate-forme connaissent déjà avec Kubernetes, et peut évoluer vers une CRD.
- **Négatif** : `cel-go` est une dépendance conséquente (ANTLR, protobuf), maintenue par Google et utilisée par Kubernetes.
- **Négatif** : CEL est une syntaxe de plus à apprendre ; [events.md](../events.md) en donne un aide-mémoire.
