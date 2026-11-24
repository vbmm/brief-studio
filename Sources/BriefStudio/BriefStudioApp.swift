import AppKit
import BriefCore
import SwiftUI

@main
struct BriefStudioApp: App {
    @StateObject private var studio = StudioModel()
    @AppStorage("releaseRepository") private var releaseRepository = ""

    init() {
        // Exercise the same export used in the window without accessing an account or market feed.
        let arguments = CommandLine.arguments
        if arguments.count == 3, arguments[1] == "--export-example" {
            do {
                try BriefFiles.html(.sample, template: BriefFiles.template())
                    .write(toFile: arguments[2], atomically: true, encoding: .utf8)
                exit(EXIT_SUCCESS)
            } catch {
                FileHandle.standardError.write(Data((error.localizedDescription + "\n").utf8))
                exit(EXIT_FAILURE)
            }
        }
    }

    var body: some Scene {
        WindowGroup("Brief Studio", id: "studio") {
            StudioWindow(studio: studio, releaseRepository: $releaseRepository)
                .frame(minWidth: 740, minHeight: 540)
                .task { studio.start(repository: releaseRepository) }
        }
        .defaultSize(width: 1010, height: 720)
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("Nouveau brief…", action: compose)
                    .keyboardShortcut("n")
                    .disabled(studio.working != nil)
                Button("Ouvrir un brief JSON…", action: studio.openFile).keyboardShortcut("o")
                Button("Exporter en HTML…") { studio.export(html: true) }
                    .keyboardShortcut("e", modifiers: [.command, .shift])
                    .disabled(studio.current == nil)
            }
            CommandGroup(after: .appInfo) {
                Button("Mises à jour…") { studio.showsUpdates = true }
            }
        }
    }

    private func compose() { studio.showsComposer = true }
}

private struct StudioWindow: View {
    @ObservedObject var studio: StudioModel
    @Binding var releaseRepository: String

    var body: some View {
        NavigationSplitView {
            List(selection: $studio.selection) {
                Section("Bibliothèque") {
                    if studio.history.isEmpty {
                        Text("Aucun brief enregistré")
                            .font(.callout).foregroundStyle(.secondary)
                            .listRowBackground(Color.clear)
                    }
                    ForEach(studio.history) { item in
                        Label {
                            VStack(alignment: .leading, spacing: 3) {
                                Text(item.document.title).lineLimit(1)
                                Text(stamp(item.document.generatedAt)).font(.caption).foregroundStyle(.secondary)
                            }
                        } icon: { Image(systemName: "doc.text") }
                        .tag(item.id)
                    }
                }
                Section("Découvrir") {
                    Label("Exemple NQ / ES", systemImage: "chart.line.uptrend.xyaxis")
                        .tag(studio.example.id)
                }
            }
            .navigationTitle("Brief Studio")
            .navigationSplitViewColumnWidth(min: 190, ideal: 222)
            .safeAreaInset(edge: .bottom) { accountBar }
        } detail: {
            Group {
                if let entry = studio.current {
                    BriefPage(document: entry.document, isExample: entry.id == studio.example.id)
                        .id(entry.id)
                } else {
                    ContentUnavailableView("Choisis un brief", systemImage: "doc.text")
                }
            }
            .toolbar {
                ToolbarItemGroup {
                    Button { studio.showsComposer = true } label: {
                        Label("Nouveau brief", systemImage: "square.and.pencil")
                    }
                    .disabled(studio.working != nil)
                    .help("Générer un brief NQ / ES")
                    Menu {
                        Button("Exporter pour Windows et navigateur…") { studio.export(html: true) }
                        Button("Exporter le JSON…") { studio.export(html: false) }
                        Divider()
                        Button("Importer un JSON…", action: studio.openFile)
                    } label: { Label("Partager", systemImage: "square.and.arrow.up") }
                    .menuIndicator(.hidden)
                    Button { studio.showsUpdates = true } label: {
                        Label("Mises à jour", systemImage: "arrow.down.circle")
                    }
                    .help(studio.update == nil ? "Mises à jour" : "Une mise à jour est disponible")
                }
            }
        }
        .sheet(isPresented: $studio.showsComposer) { Composer(studio: studio) }
        .sheet(isPresented: $studio.showsAccount) { AccountPane(studio: studio) }
        .sheet(isPresented: $studio.showsUpdates) { UpdatesPane(studio: studio, repository: $releaseRepository) }
        .alert("Brief Studio", isPresented: Binding(get: { studio.notice != nil }, set: { if !$0 { studio.notice = nil } })) {
            Button("OK") { studio.notice = nil }
        } message: { Text(studio.notice ?? "") }
    }

    private var accountBar: some View {
        Button { studio.showsAccount = true } label: {
            HStack(spacing: 9) {
                Image(systemName: studio.profile == nil ? "person.crop.circle" : "person.crop.circle.fill")
                    .font(.title3)
                    .foregroundStyle(Color.accentColor)
                VStack(alignment: .leading, spacing: 2) {
                    Text(studio.profile?.name ?? "Compte ChatGPT").font(.callout.weight(.medium)).lineLimit(1)
                    Text(studio.profile == nil ? "Se connecter" : "Connecté")
                        .foregroundStyle(.secondary).font(.caption)
                }
                Spacer(minLength: 4)
                Image(systemName: "chevron.right").font(.caption2).foregroundStyle(.secondary)
            }
            .contentShape(Rectangle())
            .padding(.horizontal, 14).padding(.vertical, 11)
        }
        .buttonStyle(.plain)
        .background(.bar)
    }
}

private func stamp(_ raw: String) -> String {
    let parser = ISO8601DateFormatter()
    let standard = parser.date(from: raw)
    parser.formatOptions.insert(.withFractionalSeconds)
    return (standard ?? parser.date(from: raw))?.formatted(date: .abbreviated, time: .shortened) ?? raw
}

private let chatGPTUsage = URL(string: "https://chatgpt.com/settings/usage")!

private struct AccountFeedback: View {
    @ObservedObject var studio: StudioModel
    @State private var copied = false

    var body: some View {
        if let url = studio.browserSignInURL {
            VStack(alignment: .leading, spacing: 9) {
                Label("Autorise Brief Studio sur la page OpenAI", systemImage: "safari")
                    .font(.callout.weight(.semibold))
                Text("Si aucun onglet ne s’ouvre, copie le lien et colle-le dans la barre d’adresse de Chrome. Il expire après trois minutes.")
                    .font(.callout).foregroundStyle(.secondary)
                HStack {
                    Link("Ouvrir dans le navigateur", destination: url).buttonStyle(.borderedProminent)
                    Button(copied ? "Lien copié" : "Copier le lien") {
                        let pasteboard = NSPasteboard.general
                        pasteboard.clearContents()
                        copied = pasteboard.setString(url.absoluteString, forType: .string)
                    }
                }
                .onChange(of: url, initial: true) { copied = false }
            }
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.quaternary.opacity(0.6), in: RoundedRectangle(cornerRadius: 10))
        }
        if let error = studio.operationError {
            HStack(alignment: .firstTextBaseline) {
                Label(error, systemImage: "exclamationmark.circle")
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
                Spacer()
                Button("Fermer le message", systemImage: "xmark") { studio.dismissOperationError() }
                    .labelStyle(.iconOnly).buttonStyle(.borderless)
            }
            .font(.callout)
        }
        if studio.showsPlanWelcome {
            HStack {
                Text("Les générations éligibles utilisent ton forfait ChatGPT.")
                Spacer()
                Button("Compris", action: studio.dismissWelcome)
            }
            .font(.callout)
        }
        if studio.showsUsageLimit {
            VStack(alignment: .leading, spacing: 6) {
                Text("Limite ChatGPT atteinte. Vérifie le forfait ou la limite de Brief Studio.")
                Link("Gérer l’utilisation", destination: chatGPTUsage)
            }
            .font(.callout)
        }
    }
}

private struct SignInControls: View {
    @ObservedObject var studio: StudioModel

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("La génération utilise ton propre forfait ChatGPT, si ton compte y est éligible.")
                .font(.callout).foregroundStyle(.secondary)
            DisclosureGroup("Confidentialité") {
                Text("La connexion s’ouvre chez OpenAI. La session est stockée dans le Trousseau macOS. L’app transmet à OpenAI les observations et ta note lors de la génération. Les briefs restent sur ce Mac et tout export inclut la note saisie. Tu peux révoquer l’accès dans ChatGPT.")
                    .font(.callout).foregroundStyle(.secondary).padding(.top, 6)
            }
            Button("Continuer avec ChatGPT") { studio.signIn() }.buttonStyle(.borderedProminent)
            if !studio.savedProfiles.isEmpty {
                Menu("Choisir un autre compte") {
                    ForEach(studio.savedProfiles) { profile in
                        Button((profile.email ?? profile.name ?? "ChatGPT") + " · " + profile.id.suffix(6)) {
                            studio.selectProfile(profile.id)
                        }
                    }
                    Divider()
                    Button("Connecter un autre compte") { studio.signIn(newAccount: true) }
                }
            }
        }
        .disabled(studio.working != nil)
    }
}

private struct BriefPage: View {
    let document: BriefDocument
    let isExample: Bool

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                VStack(alignment: .leading, spacing: 11) {
                    HStack(spacing: 6) {
                        Image(systemName: "waveform.path")
                        Text("NQ  /  ES")
                    }
                    .font(.system(.caption, design: .monospaced).weight(.semibold))
                    .foregroundStyle(Color.accentColor)
                    Text(document.title)
                        .font(.system(size: 36, weight: .semibold, design: .serif))
                        .tracking(-1)
                        .textSelection(.enabled)
                    Label(isExample ? "Exemple · données fictives" : stamp(document.generatedAt),
                          systemImage: isExample ? "doc.text.magnifyingglass" : "clock")
                        .font(.callout).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                Divider()
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(Array(document.text.split(separator: "\n").enumerated()), id: \.offset) { _, raw in
                        let line = String(raw).trimmingCharacters(in: .whitespaces)
                        if line.hasPrefix("# ") && String(line.dropFirst(2)) == document.title {
                            EmptyView()
                        } else if line.hasPrefix("#") {
                            Text(line.drop(while: { $0 == "#" || $0 == " " }))
                                .font(.system(.title3, design: .serif).weight(.semibold)).padding(.top, 11)
                        } else if line == "---" {
                            Divider().padding(.vertical, 2)
                        } else if line.hasPrefix("- ") || line.hasPrefix("* ") {
                            HStack(alignment: .firstTextBaseline, spacing: 8) {
                                Text("•").foregroundStyle(Color.accentColor)
                                Text(inline(String(line.dropFirst(2)))).lineSpacing(4)
                            }
                        } else {
                            Text(inline(line)).lineSpacing(4)
                        }
                    }
                }
                .font(.body)
                .textSelection(.enabled)
                let links = (document.sources ?? []).compactMap { source -> (String, URL)? in
                    guard let parts = URLComponents(string: source.url), parts.scheme?.lowercased() == "https",
                          parts.host != nil, parts.user == nil, parts.password == nil, let url = parts.url else { return nil }
                    return (source.label, url)
                }
                if !links.isEmpty {
                    VStack(alignment: .leading, spacing: 9) {
                        Text("Sources").font(.headline)
                        ForEach(Array(links.enumerated()), id: \.offset) { _, link in
                            Link(destination: link.1) { Label(link.0, systemImage: "arrow.up.right") }
                                .font(.callout)
                        }
                    }
                    .padding(16)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(.quaternary.opacity(0.6), in: RoundedRectangle(cornerRadius: 12))
                }
            }
            .frame(maxWidth: 650, alignment: .leading)
            .padding(.horizontal, 40).padding(.vertical, 32)
            .frame(maxWidth: .infinity)
        }
        .background(Color(nsColor: .textBackgroundColor))
    }

    private func inline(_ line: String) -> AttributedString {
        var result = (try? AttributedString(markdown: line, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(line)
        // Source URLs have their own validated list. Imported prose cannot open custom URL schemes.
        result.link = nil
        return result
    }
}

private struct Composer: View {
    @ObservedObject var studio: StudioModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Nouveau brief").font(.title2.bold())
            Text("Futures NQ et ES · prix récents et lecture de marché en français")
                .font(.callout).foregroundStyle(.secondary)
            if studio.profile == nil {
                SignInControls(studio: studio)
            } else if studio.profile?.canUsePlan != true {
                Text("Ce compte n’a pas accès à la génération dans l’app.").foregroundStyle(.secondary)
                Button("Changer de compte", action: studio.signOut).disabled(studio.working != nil)
            } else {
                if studio.models.isEmpty {
                    Button("Charger les modèles disponibles", action: studio.refreshModels).disabled(studio.working != nil)
                } else {
                    Picker("Modèle", selection: $studio.selectedModel) {
                        ForEach(studio.models, id: \.id) { model in Text(model.displayName).tag(model.id) }
                    }
                }
                HStack {
                    Text("Forfait ChatGPT utilisé").foregroundStyle(.secondary)
                    Spacer()
                    Link("Gérer l’utilisation", destination: chatGPTUsage)
                }
                .font(.caption)
                VStack(alignment: .leading, spacing: 7) {
                    Text("Contexte facultatif").font(.headline)
                    TextField("Ex. publication macro à surveiller", text: $studio.context, axis: .vertical)
                        .lineLimit(3...5)
                    TextField("Lien HTTPS de la source", text: $studio.sourceURL)
                    Text("Les données résumées et ce contexte seront envoyés à ChatGPT lors de la génération.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .disabled(studio.working != nil)
            }
            AccountFeedback(studio: studio)
            Divider()
            HStack {
                if let label = studio.working {
                    ProgressView().controlSize(.small)
                    Text(label).foregroundStyle(.secondary).font(.callout)
                }
                Spacer()
                if studio.working != nil {
                    Button("Annuler", action: studio.cancel).keyboardShortcut(.cancelAction)
                } else {
                    Button("Fermer") { dismiss() }.keyboardShortcut(.cancelAction)
                    Button("Générer", action: studio.generate)
                        .keyboardShortcut(.defaultAction).buttonStyle(.borderedProminent)
                        .disabled(studio.models.isEmpty || studio.profile?.canUsePlan != true)
                }
            }
        }
        .padding(24).frame(width: 495)
        .interactiveDismissDisabled(studio.working != nil)
        .task { if studio.profile?.canUsePlan == true, studio.models.isEmpty { studio.refreshModels() } }
    }
}

private struct AccountPane: View {
    @ObservedObject var studio: StudioModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Compte ChatGPT").font(.title2.bold())
            if let profile = studio.profile {
                Text(profile.email ?? profile.name ?? "Connecté")
                Text(profile.canUsePlan ? "Ce compte permet la génération." : "L’accès à la génération dépend du compte ou de l’espace de travail.")
                    .font(.callout).foregroundStyle(.secondary)
                Link("Gérer l’utilisation", destination: chatGPTUsage).font(.callout)
                HStack {
                    Menu("Compte") {
                        Button("Reconnecter") { studio.signIn() }
                        ForEach(studio.savedProfiles.filter { $0.id != profile.id }) { saved in
                            Button((saved.email ?? saved.name ?? "ChatGPT") + " · " + saved.id.suffix(6)) {
                                studio.selectProfile(saved.id)
                            }
                        }
                        Button("Connecter un autre compte") { studio.signIn(newAccount: true) }
                    }
                    Button("Déconnecter", action: studio.signOut)
                }
                .disabled(studio.working != nil)
            } else {
                SignInControls(studio: studio)
            }
            AccountFeedback(studio: studio)
            if let label = studio.working {
                HStack {
                    ProgressView().controlSize(.small)
                    Text(label)
                    Spacer()
                    Button("Annuler", action: studio.cancel).keyboardShortcut(.cancelAction)
                }
            }
            HStack { Spacer(); Button("Fermer") { dismiss() }.disabled(studio.working != nil) }
        }
        .padding(24).frame(width: 420)
        .interactiveDismissDisabled(studio.working != nil)
    }
}

private struct UpdatesPane: View {
    @ObservedObject var studio: StudioModel
    @Binding var repository: String
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Mises à jour").font(.title2.bold())
            Text("Version installée \(studio.version)").foregroundStyle(.secondary)
            if studio.releaseRepository.isEmpty {
                TextField("Dépôt GitHub (propriétaire/dépôt)", text: $repository)
                    .disabled(studio.checkingUpdate)
            } else {
                Label(studio.releaseRepository, systemImage: "shippingbox")
            }
            if studio.checkingUpdate { ProgressView().controlSize(.small) }
            else if !studio.updateStatus.isEmpty { Text(studio.updateStatus).font(.callout) }
            if let update = studio.update { Link("Ouvrir la version \(update.tag) sur GitHub", destination: update.releasePage) }
            HStack {
                Button("Vérifier") { Task { await studio.checkUpdates(repository: repository) } }
                    .disabled(studio.checkingUpdate || (repository.isEmpty && studio.releaseRepository.isEmpty))
                Spacer()
                Button("Fermer") { dismiss() }.keyboardShortcut(.cancelAction)
            }
        }
        .padding(24).frame(width: 435)
        .task {
            if !repository.isEmpty || !studio.releaseRepository.isEmpty {
                await studio.checkUpdates(repository: repository)
            }
        }
    }
}
