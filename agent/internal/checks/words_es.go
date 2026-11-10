package checks

// spanish — тревоги по-испански (трек AE, спринт 214): нейтральный
// испанский на «tú», названия экранов — как в меню приложения (`es.json`).
var spanish = Words{
	DiskSubject: "El disco del servidor está lleno al %d %%",
	DiskBody: "Está usado el %d %% del disco: quedan %d MB libres de %d MB.\n\n" +
		"Cuando se acabe el espacio, se detendrán tanto la analítica como el sitio.",
	DiskOKSubject: "El espacio en el disco del servidor vuelve a estar bien",
	DiskOKBody:    "Usado el %d %%, %d MB libres.",

	CertProblemSubject: "Certificado %s: hay un problema",
	CertProblemBody: "La verificación del certificado falló: %s.\n\n" +
		"Los visitantes del sitio ven una advertencia del navegador.",
	CertExpiringSubject: "El certificado %s vence en %d días",
	CertExpiringBody: "El certificado es válido hasta el %s.\n\n" +
		"Si lo emitió la supervisión de vpsFocus, se renueva automáticamente: " +
		"comprueba que el dominio siga apuntando al servidor.",
	CertOKSubject: "El certificado %s vuelve a estar bien",
	CertOKBody:    "La verificación se supera y el vencimiento no está cerca.",

	DomainExpiringSubject: "El dominio %s vence en %d días",
	DomainExpiringBody: "El registro del dominio es válido hasta el %s.\n\n" +
		"La renovación se hace en tu registrador%s.",
	DomainRenewedSubject: "Dominio %s renovado",
	DomainRenewedBody:    "El registro es válido hasta el %s.",

	LogsSubject: "El servidor devuelve errores: %d en %d min",
	LogsBody: "En los últimos %d minutos, los registros del servidor web acumularon %d respuestas " +
		"con código 5xx, por encima del umbral de %d.\n\nQué falla exactamente se ve en la pantalla " +
		"«Errores» de la aplicación: allí los errores están agrupados por dirección y código.",
	LogsOKSubject: "Los errores del servidor se detuvieron",
	LogsOKBody:    "En los últimos %d minutos, los registros del servidor web tienen %d respuestas con código 5xx: por debajo del umbral de %d.",

	ProbeSubject: "Una dirección clave no funciona: %s",
	ProbeBody: "La verificación «%s» en %s falló: %s.\n\n" +
		"La verificación se hace desde el propio servidor, así que responde a " +
		"«¿responde la aplicación?», no a «¿lo ve un visitante?»: el cortafuegos " +
		"en la nube del proveedor de hosting no se ve desde aquí.",
	ProbeOKSubject: "La dirección clave vuelve a funcionar: %s",
	ProbeOKBody:    "La verificación «%s» en %s vuelve a superarse.",

	PortSubject: "El puerto ya no escucha: %s (%s)",
	PortBody: "Ya nada escucha en el puerto %s del servidor (verificación «%s»): el " +
		"servicio que funcionaba en él se detuvo o pasó a otro puerto.\n\n" +
		"La verificación lee la propia tabla de puertos del servidor: dice si el " +
		"puerto está ocupado, no si el servicio responde bien. Un servicio en un " +
		"contenedor sin puerto publicado no se ve desde aquí.",
	PortOKSubject: "El puerto vuelve a escuchar: %s (%s)",
	PortOKBody:    "Un servicio vuelve a escuchar en el puerto %s del servidor (verificación «%s»).",

	BackupSubject: "La copia de la base de datos falló",
	BackupBody: "La última copia de la analítica no llegó al almacenamiento: %s.\n\n" +
		"Mientras no haya copias, perder el disco de este servidor significa perder " +
		"todas las estadísticas. La configuración del almacenamiento está en la " +
		"aplicación, en la pantalla «Notificaciones»; qué falló se ve en la pantalla " +
		"«Seguridad» del servidor.",
	BackupOKSubject: "Las copias de la base de datos vuelven a funcionar",
	BackupOKBody:    "La copia de la analítica llegó al almacenamiento: %s, %d bytes.",

	InodesSubject: "El servidor se está quedando sin límite de archivos: usado el %d %%",
	InodesBody: "Está usado el %d %% del límite de archivos del disco (inodos); quedan %d.\n\n" +
		"Puede que todavía haya espacio libre en el disco, pero no se podrá crear " +
		"ningún archivo nuevo: el sitio dejará de guardar sesiones, subidas y caché. " +
		"Normalmente la culpa es de una carpeta con muchísimos archivos pequeños: " +
		"caché, sesiones, correo antiguo. Los detalles están en la pantalla «Estado " +
		"del servidor» de la aplicación.",
	InodesOKSubject: "El límite de archivos del servidor vuelve a estar bien",
	InodesOKBody:    "Está usado el %d %% del límite de archivos del disco.",

	BackupShrunkSubject: "La copia de la base de datos es mucho más pequeña que la anterior",
	BackupShrunkBody: "La copia de la analítica se hizo y se comprobó, pero contiene menos de la " +
		"mitad que la anterior: antes %s MB, ahora %s MB.\n\n" +
		"Ocurre después de limpiar estadísticas antiguas, y entonces no hay que hacer nada. " +
		"Si no eliminaste nada, comprueba que no falten datos: el historial de copias " +
		"está en la pantalla «Copias de seguridad» de la aplicación.",
	BackupShrunkOKSubject: "La copia de la base de datos vuelve a tener su tamaño habitual",
	BackupShrunkOKBody:    "La última copia de la analítica ocupa %s MB, no menos de la mitad que la anterior.",

	TestSubject: "Prueba de notificaciones",
	TestBody: "Este es un mensaje de prueba del servidor %s.\n\n" +
		"Si lo estás leyendo, las notificaciones sobre el disco, los certificados " +
		"y los dominios llegarán por el mismo camino.",

	MailBandProblem: "Problema",
	MailBandWarning: "Aviso",
	MailBandOK:      "De nuevo en orden",
	MailSource:      "servidor %s",
	MailFooter: "Este correo lo envió la supervisión de vpsFocus desde el servidor %s, a " +
		"través del correo indicado en «Canales de notificación», no a través de " +
		"nosotros. Para desactivar las notificaciones de este servidor: aplicación " +
		"vpsFocus → «Todos los servidores» → «Configuración» → «Notificaciones», " +
		"bloque «Qué servidores envían notificaciones».",

	Decimal: ",",
}
