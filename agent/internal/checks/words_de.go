package checks

// german — тревоги по-немецки (трек AE, спринт 221): на «Sie», названия
// экранов — как в меню приложения (`de.json`).
var german = Words{
	DiskSubject: "Die Festplatte des Servers ist zu %d %% voll",
	DiskBody: "%d %% der Festplatte sind belegt: %d MB frei von %d MB.\n\n" +
		"Ist der Platz aufgebraucht, halten sowohl die Statistik als auch die Website an.",
	DiskOKSubject: "Der Platz auf der Festplatte des Servers ist wieder in Ordnung",
	DiskOKBody:    "%d %% belegt, %d MB frei.",

	CertProblemSubject: "Zertifikat %s: ein Problem",
	CertProblemBody: "Die Prüfung des Zertifikats ist fehlgeschlagen: %s.\n\n" +
		"Besucher der Website sehen eine Warnung des Browsers.",
	CertExpiringSubject: "Das Zertifikat %s läuft in %d Tagen ab",
	CertExpiringBody: "Das Zertifikat ist gültig bis %s.\n\n" +
		"Hat es die Überwachung von vpsFocus ausgestellt, erneuert es sich automatisch: " +
		"Prüfen Sie, ob die Domain noch auf den Server zeigt.",
	CertOKSubject: "Das Zertifikat %s ist wieder in Ordnung",
	CertOKBody:    "Die Prüfung besteht, und der Ablauf ist nicht nah.",

	DomainExpiringSubject: "Die Domain %s läuft in %d Tagen ab",
	DomainExpiringBody: "Die Registrierung der Domain ist gültig bis %s.\n\n" +
		"Verlängert wird bei Ihrem Registrar%s.",
	DomainRenewedSubject: "Domain %s verlängert",
	DomainRenewedBody:    "Die Registrierung ist gültig bis %s.",

	LogsSubject: "Der Server liefert Fehler: %d in %d Min.",
	LogsBody: "In den letzten %d Minuten haben sich in den Logs des Webservers %d Antworten " +
		"mit Code 5xx angesammelt, über der Schwelle von %d.\n\nWas genau ausfällt, sehen Sie in " +
		"der App auf dem Bildschirm „Fehler“: Dort sind die Fehler nach Adresse und Code gruppiert.",
	LogsOKSubject: "Die Serverfehler haben aufgehört",
	LogsOKBody:    "In den letzten %d Minuten haben die Logs des Webservers %d Antworten mit Code 5xx — unter der Schwelle von %d.",

	ProbeSubject: "Eine Schlüsseladresse funktioniert nicht: %s",
	ProbeBody: "Die Prüfung „%s“ unter %s ist fehlgeschlagen: %s.\n\n" +
		"Die Prüfung läuft vom Server selbst aus, sie beantwortet also „antwortet " +
		"die Anwendung?“, nicht „sieht ein Besucher sie?“: Die Cloud-Firewall des " +
		"Hosting-Anbieters ist von hier aus nicht sichtbar.",
	ProbeOKSubject: "Die Schlüsseladresse funktioniert wieder: %s",
	ProbeOKBody:    "Die Prüfung „%s“ unter %s besteht wieder.",

	PortSubject: "Der Port lauscht nicht mehr: %s (%s)",
	PortBody: "Auf Port %s des Servers lauscht nichts mehr (Prüfung „%s“): Der " +
		"Dienst, der dort lief, ist angehalten oder auf einen anderen Port umgezogen.\n\n" +
		"Die Prüfung liest die eigene Portliste des Servers: Sie sagt, ob der " +
		"Port belegt ist, nicht, ob der Dienst richtig antwortet. Ein Dienst in " +
		"einem Container ohne veröffentlichten Port ist von hier aus nicht sichtbar.",
	PortOKSubject: "Der Port lauscht wieder: %s (%s)",
	PortOKBody:    "Auf Port %s des Servers lauscht wieder ein Dienst (Prüfung „%s“).",

	BackupSubject: "Die Kopie der Datenbank ist fehlgeschlagen",
	BackupBody: "Die letzte Kopie der Statistik ist nicht im Speicher angekommen: %s.\n\n" +
		"Solange es keine Kopien gibt, bedeutet der Verlust der Festplatte dieses " +
		"Servers den Verlust der gesamten Statistik. Die Einstellungen des Speichers " +
		"stehen in der App auf dem Bildschirm „Benachrichtigungen“; was schiefgelaufen " +
		"ist, zeigt der Bildschirm „Sicherheit“ des Servers.",
	BackupOKSubject: "Die Kopien der Datenbank funktionieren wieder",
	BackupOKBody:    "Die Kopie der Statistik ist im Speicher angekommen: %s, %d Bytes.",

	InodesSubject: "Dem Server geht das Dateilimit aus: %d %% belegt",
	InodesBody: "%d %% des Dateilimits der Festplatte (Inodes) sind belegt, %d übrig.\n\n" +
		"Auf der Festplatte ist vielleicht noch Platz frei, aber es lässt sich keine " +
		"neue Datei mehr anlegen: Die Website speichert keine Sitzungen, Uploads und " +
		"keinen Cache mehr. Meist ist ein Ordner mit sehr vielen kleinen Dateien " +
		"schuld: Cache, Sitzungen, alte E-Mails. Details stehen in der App auf dem " +
		"Bildschirm „Serverzustand“.",
	InodesOKSubject: "Das Dateilimit des Servers ist wieder in Ordnung",
	InodesOKBody:    "%d %% des Dateilimits der Festplatte sind belegt.",

	BackupShrunkSubject: "Die Kopie der Datenbank ist viel kleiner als die vorige",
	BackupShrunkBody: "Die Kopie der Statistik wurde erstellt und geprüft, enthält aber weniger als " +
		"die Hälfte der vorigen: vorher %s MB, jetzt %s MB.\n\n" +
		"Das passiert, nachdem alte Statistik aufgeräumt wurde — dann ist nichts zu tun. " +
		"Haben Sie nichts gelöscht, prüfen Sie, ob keine Daten fehlen: Der Verlauf der " +
		"Kopien steht in der App auf dem Bildschirm „Sicherungen“.",
	BackupShrunkOKSubject: "Die Kopie der Datenbank hat wieder ihre übliche Größe",
	BackupShrunkOKBody:    "Die letzte Kopie der Statistik umfasst %s MB, nicht weniger als die Hälfte der vorigen.",

	TestSubject: "Test der Benachrichtigungen",
	TestBody: "Das ist eine Testnachricht vom Server %s.\n\n" +
		"Wenn Sie sie lesen, kommen Benachrichtigungen über Festplatte, Zertifikate " +
		"und Domains auf demselben Weg an.",

	MailBandProblem: "Problem",
	MailBandWarning: "Hinweis",
	MailBandOK:      "Wieder in Ordnung",
	MailSource:      "Server %s",
	MailFooter: "Diese E-Mail hat die Überwachung von vpsFocus vom Server %s gesendet — über " +
		"das unter „Benachrichtigungskanäle“ eingetragene Postfach, nicht über uns. " +
		"Um Benachrichtigungen dieses Servers auszuschalten: App vpsFocus → „Alle Server“ → " +
		"„Einstellungen“ → „Benachrichtigungen“, Block „Welche Server Benachrichtigungen senden“.",

	Decimal: ",",
}
