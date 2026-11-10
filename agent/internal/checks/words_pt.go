package checks

// portuguese — todas as mensagens em português do Brasil (трек AE, спринт 235):
// em «você», nomes das telas — como no menu do app (`pt.json`).
var portuguese = Words{
	DiskSubject: "O disco do servidor está %d%% cheio",
	DiskBody: "%d%% do disco estão em uso: %d MB livres de %d MB.\n\n" +
		"Quando o espaço acaba, tanto as estatísticas quanto o site param.",
	DiskOKSubject: "O espaço em disco do servidor voltou ao normal",
	DiskOKBody:    "%d%% em uso, %d MB livres.",

	CertProblemSubject: "Problema no certificado de %s",
	CertProblemBody: "A verificação do certificado falhou: %s.\n\n" +
		"Os visitantes do site veem um aviso do navegador.",
	CertExpiringSubject: "O certificado de %s vence em %d dias",
	CertExpiringBody: "O certificado é válido até %s.\n\n" +
		"Se ele foi emitido pelo monitoramento do vpsFocus, é renovado sozinho: " +
		"confira se o domínio continua apontando para o servidor.",
	CertOKSubject: "O certificado de %s voltou ao normal",
	CertOKBody:    "A verificação foi bem-sucedida e o vencimento não está próximo.",

	DomainExpiringSubject: "O domínio %s vence em %d dias",
	DomainExpiringBody: "O registro do domínio é válido até %s.\n\n" +
		"A renovação é feita no seu registrador%s.",
	DomainRenewedSubject: "Domínio %s renovado",
	DomainRenewedBody:    "O registro é válido até %s.",

	LogsSubject: "O servidor está devolvendo erros: %d em %d min",
	LogsBody: "Nos últimos %d minutos, os logs do servidor web acumularam %d respostas " +
		"com código 5xx, acima do limite de %d.\n\nO que exatamente está falhando aparece no " +
		"app, na tela “Erros”: lá os erros são agrupados por endereço e por código.",
	LogsOKSubject: "Os erros do servidor pararam",
	LogsOKBody:    "Nos últimos %d minutos, os logs do servidor web têm %d respostas com código 5xx, abaixo do limite de %d.",

	ProbeSubject: "Um endereço-chave não está funcionando: %s",
	ProbeBody: "A verificação “%s” no endereço %s falhou: %s.\n\n" +
		"A verificação roda no próprio servidor: ela responde à pergunta " +
		"“o app responde?”, e não “um visitante o enxerga?”. O firewall " +
		"na nuvem da hospedagem não é visível daqui.",
	ProbeOKSubject: "O endereço-chave voltou a funcionar: %s",
	ProbeOKBody:    "A verificação “%s” no endereço %s voltou a ser bem-sucedida.",

	PortSubject: "A porta deixou de escutar: %s (%s)",
	PortBody: "Nada mais escuta na porta %s do servidor (verificação “%s”): o " +
		"serviço que funcionava nela parou ou mudou de porta.\n\n" +
		"A verificação lê a lista de portas do próprio servidor: ela mostra se a " +
		"porta está ocupada, e não se o serviço responde corretamente. Um serviço em " +
		"um contêiner sem porta publicada não é visível daqui.",
	PortOKSubject: "A porta voltou a escutar: %s (%s)",
	PortOKBody:    "Um serviço voltou a escutar na porta %s do servidor (verificação “%s”).",

	BackupSubject: "A cópia do banco de dados falhou",
	BackupBody: "A última cópia das estatísticas não chegou ao armazenamento: %s.\n\n" +
		"Sem cópias, perder o disco deste servidor significa perder " +
		"todas as estatísticas. As configurações do armazenamento ficam no app, " +
		"na tela “Notificações”; o que não funcionou aparece na tela " +
		"“Segurança” do servidor.",
	BackupOKSubject: "As cópias do banco de dados voltaram a funcionar",
	BackupOKBody:    "A cópia das estatísticas chegou ao armazenamento: %s, %d bytes.",

	InodesSubject: "O servidor está chegando ao limite de arquivos: %d%% em uso",
	InodesBody: "%d%% do limite de arquivos do disco (inodes) estão em uso, restam %d.\n\n" +
		"Pode ainda haver espaço livre no disco, mas nenhum arquivo novo pode " +
		"ser criado: o site deixará de gravar sessões, arquivos enviados e cache. " +
		"Em geral, a causa é uma pasta com um número enorme de arquivos pequenos: " +
		"cache, sessões, e-mails antigos. Os detalhes estão no app, na tela " +
		"“Saúde do servidor”.",
	InodesOKSubject: "O limite de arquivos do servidor voltou ao normal",
	InodesOKBody:    "%d%% do limite de arquivos do disco estão em uso.",

	BackupShrunkSubject: "A cópia do banco de dados é bem menor que a anterior",
	BackupShrunkBody: "A cópia das estatísticas foi feita e verificada, mas tem menos da " +
		"metade da anterior: era %s MB, agora é %s MB.\n\n" +
		"Isso acontece depois de limpar estatísticas antigas: então não há nada a fazer. " +
		"Se você não excluiu nada, confira se não está faltando algum dado: o histórico das " +
		"cópias está no app, na tela “Backups”.",
	BackupShrunkOKSubject: "A cópia do banco de dados voltou ao tamanho habitual",
	BackupShrunkOKBody:    "A última cópia das estatísticas tem %s MB, não menos da metade da anterior.",

	TestSubject: "Teste de notificações",
	TestBody: "Esta é uma mensagem de teste do servidor %s.\n\n" +
		"Se você a está lendo, as notificações sobre disco, certificados e " +
		"domínios chegarão pelo mesmo caminho.",

	MailBandProblem: "Problema",
	MailBandWarning: "Para observar",
	MailBandOK:      "De volta ao normal",
	MailSource:      "servidor %s",
	MailFooter: "Este e-mail foi enviado pelo monitoramento do vpsFocus a partir do servidor %s, pela " +
		"caixa indicada em “Canais de notificação”, e não por nós. Para desativar " +
		"as notificações deste servidor: app vpsFocus → “Todos os servidores” → " +
		"“Configurações” → “Notificações”, bloco “Quais servidores enviam notificações”.",

	Decimal: ",",
}
