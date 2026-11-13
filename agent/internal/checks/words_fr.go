package checks

// french — tous les messages en français (трек AE, спринт 228): на «vous»,
// названия экранов — как в меню приложения (`fr.json`).
var french = Words{
	DiskSubject: "Le disque du serveur est plein à %d %%",
	DiskBody: "%d %% du disque sont utilisés : %d Mo libres sur %d Mo.\n\n" +
		"Quand l'espace est épuisé, les statistiques comme le site s'arrêtent.",
	DiskOKSubject: "L'espace disque du serveur est revenu à la normale",
	DiskOKBody:    "%d %% utilisés, %d Mo libres.",

	CertProblemSubject: "Certificat %s : un problème",
	CertProblemBody: "La vérification du certificat a échoué : %s.\n\n" +
		"Les visiteurs du site voient un avertissement du navigateur.",
	CertExpiringSubject: "Le certificat %s expire dans %d jours",
	CertExpiringBody: "Le certificat est valable jusqu'au %s.\n\n" +
		"S'il a été délivré par la surveillance vpsFocus, il se renouvelle automatiquement : " +
		"vérifiez que le domaine pointe toujours vers le serveur.",
	CertOKSubject: "Le certificat %s est de nouveau en ordre",
	CertOKBody:    "La vérification réussit, et l'expiration n'est pas proche.",

	DomainExpiringSubject: "Le domaine %s expire dans %d jours",
	DomainExpiringBody: "L'enregistrement du domaine est valable jusqu'au %s.\n\n" +
		"Le renouvellement se fait chez votre bureau d'enregistrement%s.",
	DomainRenewedSubject: "Domaine %s renouvelé",
	DomainRenewedBody:    "L'enregistrement est valable jusqu'au %s.",

	LogsSubject: "Le serveur renvoie des erreurs : %d en %d min",
	LogsBody: "Au cours des %d dernières minutes, les journaux du serveur web ont accumulé %d réponses " +
		"avec un code 5xx, au-dessus du seuil de %d.\n\nCe qui échoue exactement est visible dans " +
		"l'application, sur l'écran « Erreurs » : les erreurs y sont regroupées par adresse et par code.",
	LogsOKSubject: "Les erreurs du serveur ont cessé",
	LogsOKBody:    "Au cours des %d dernières minutes, les journaux du serveur web contiennent %d réponses avec un code 5xx, sous le seuil de %d.",

	ProbeSubject: "Une adresse clé ne fonctionne pas : %s",
	ProbeBody: "La vérification « %s » à l'adresse %s a échoué : %s.\n\n" +
		"La vérification s'exécute depuis le serveur lui-même : elle répond donc à la question " +
		"« l'application répond-elle ? », et non « un visiteur la voit-il ? ». Le pare-feu " +
		"cloud de l'hébergeur n'est pas visible d'ici.",
	ProbeOKSubject: "L'adresse clé fonctionne à nouveau : %s",
	ProbeOKBody:    "La vérification « %s » à l'adresse %s réussit à nouveau.",

	PortSubject: "Le port n'écoute plus : %s (%s)",
	PortBody: "Plus rien n'écoute sur le port %s du serveur (vérification « %s ») : le " +
		"service qui y fonctionnait s'est arrêté ou a changé de port.\n\n" +
		"La vérification lit la liste des ports du serveur lui-même : elle indique si le " +
		"port est occupé, et non si le service répond correctement. Un service dans " +
		"un conteneur sans port publié n'est pas visible d'ici.",
	PortOKSubject: "Le port écoute à nouveau : %s (%s)",
	PortOKBody:    "Un service écoute de nouveau sur le port %s du serveur (vérification « %s »).",

	BackupSubject: "La copie de la base de données a échoué",
	BackupBody: "La dernière copie des statistiques n'est pas arrivée dans le stockage : %s.\n\n" +
		"Tant qu'il n'y a pas de copies, perdre le disque de ce serveur signifie perdre " +
		"toutes les statistiques. Les réglages du stockage se trouvent dans l'application, " +
		"sur l'écran « Notifications » ; ce qui n'a pas fonctionné est indiqué sur l'écran " +
		"« Sécurité » du serveur.",
	BackupOKSubject: "Les copies de la base de données fonctionnent à nouveau",
	BackupOKBody:    "La copie des statistiques est arrivée dans le stockage : %s, %d octets.",

	InodesSubject: "Le serveur arrive au bout de sa limite de fichiers : %d %% utilisés",
	InodesBody: "%d %% de la limite de fichiers du disque (inodes) sont utilisés, il en reste %d.\n\n" +
		"Il reste peut-être de l'espace libre sur le disque, mais aucun nouveau fichier ne peut " +
		"être créé : le site cessera d'enregistrer les sessions, les fichiers envoyés et le cache. " +
		"En général, un dossier contenant un très grand nombre de petits fichiers est en cause : " +
		"cache, sessions, anciens e-mails. Les détails figurent dans l'application, sur l'écran " +
		"« État du serveur ».",
	InodesOKSubject: "La limite de fichiers du serveur est revenue à la normale",
	InodesOKBody:    "%d %% de la limite de fichiers du disque sont utilisés.",

	BackupShrunkSubject: "La copie de la base de données est bien plus petite que la précédente",
	BackupShrunkBody: "La copie des statistiques a été faite et vérifiée, mais elle contient moins de " +
		"la moitié de la précédente : %s Mo auparavant, %s Mo maintenant.\n\n" +
		"Cela arrive après le nettoyage d'anciennes statistiques : il n'y a alors rien à faire. " +
		"Si vous n'avez rien supprimé, vérifiez qu'aucune donnée ne manque : l'historique des " +
		"copies se trouve dans l'application, sur l'écran « Sauvegardes ».",
	BackupShrunkOKSubject: "La copie de la base de données a retrouvé sa taille habituelle",
	BackupShrunkOKBody:    "La dernière copie des statistiques fait %s Mo, pas moins de la moitié de la précédente.",

	OomSubject: "Le serveur a manqué de mémoire, et le système a arrêté un programme",
	OomBody: "Au cours du dernier jour, le système a arrêté des programmes faute de mémoire : " +
		"%d arrêt(s). Cela arrive quand tout le serveur manque de mémoire, ou quand un programme " +
		"atteint la limite de mémoire qui lui est fixée. Le site peut alors clignoter ou tomber.\n\n%s\n\n" +
		"Que faire : ouvrez « État du serveur » dans l’application — vous y voyez la mémoire utilisée " +
		"et la part qu’en prend la surveillance vpsFocus elle-même, ainsi qu’une tâche pour un assistant IA. " +
		"Ajouter de la mémoire dans l’offre de l’hébergeur ou activer le swap aide en général.",
	OomNames:        "Juste après, les programmes qui utilisaient le plus de mémoire étaient : %s.",
	OomNamesUnknown: "D’ici, on ne voit pas quels programmes utilisaient la mémoire.",
	OomProcess:      "%s (%d Mo)",

	RecordGoneSubject: "%s : l’enregistrement %s a disparu",
	RecordGoneBody: "Le domaine n’a plus d’enregistrement %s valide (%s) : il a été supprimé, ou il y en a désormais deux — les services de messagerie traitent cela comme « aucun enregistrement ».\n\n" +
		"Si des e-mails partent de ce domaine — inscriptions, commandes, lettres d’information —, ils risquent d’arriver en spam. Si vous avez modifié les enregistrements chez le registraire volontairement, il n’y a rien à faire. " +
		"Ce que le domaine contient maintenant est affiché dans l’écran « Domaines et SSL » de l’application, avec une tâche pour un assistant IA.",
	RecordBackSubject:  "%s : l’enregistrement %s est revenu",
	RecordBackBody:     "L’enregistrement %s de %s est de nouveau présent et lisible.",
	NameserversSubject: "%s : les serveurs de noms ont changé",
	NameserversBody: "Le domaine %s est désormais servi par d’autres serveurs de noms : avant — %s, maintenant — %s.\n\n" +
		"Cela arrive quand un domaine change de registraire ou est connecté à Cloudflare. Si ce n’est pas vous, vérifiez l’accès au panneau de votre registraire : " +
		"les serveurs de noms décident où mène le domaine et où arrive son courrier. Le résolveur du serveur garde les anciens enregistrements en mémoire, le changement apparaît donc avec un retard d’un ou deux jours.",

	TestSubject: "Test des notifications",
	TestBody: "Ceci est un message de test du serveur %s.\n\n" +
		"Si vous le lisez, les notifications sur le disque, les certificats et " +
		"les domaines arriveront par le même chemin.",

	MailBandProblem: "Problème",
	MailBandWarning: "À noter",
	MailBandOK:      "Retour à la normale",
	MailSource:      "serveur %s",
	MailFooter: "Cet e-mail a été envoyé par la surveillance vpsFocus depuis le serveur %s, par " +
		"la boîte indiquée dans « Canaux de notification », et non par nous. Pour désactiver " +
		"les notifications de ce serveur : application vpsFocus → « Tous les serveurs » → " +
		"« Paramètres » → « Notifications », bloc « Quels serveurs envoient des notifications ».",

	Decimal: ",",
}
