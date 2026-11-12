package app.talaria.ui.screens

import app.talaria.ui.theme.Ti

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import app.talaria.ui.components.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import app.talaria.ui.components.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.autofill.ContentType
import androidx.compose.ui.platform.LocalAutofillManager
import androidx.compose.ui.semantics.contentType
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import app.talaria.data.Repo
import app.talaria.data.Servers
import app.talaria.ui.components.ErrorNote
import app.talaria.ui.components.Gap
import app.talaria.ui.components.TextButton
import app.talaria.ui.components.TalariaMark
import kotlinx.coroutines.launch

@Composable
fun ConnectScreen(repo: Repo, notice: String? = null) {
    val adding by repo.adding.collectAsState()
    // Adding a second server starts from an empty address; signing back in to the only one remembers it.
    var address by rememberSaveable { mutableStateOf(if (repo.adding.value) "" else repo.store.lastServers.ifEmpty { repo.store.baseUrl }) }
    var password by rememberSaveable { mutableStateOf("") }
    var show by rememberSaveable { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    val focus = LocalFocusManager.current
    val autofill = LocalAutofillManager.current

    fun submit() {
        if (busy) return
        error = null; busy = true; focus.clearFocus()
        scope.launch {
            try {
                val origin = Servers.normalize(address)
                if (!repo.api.probe(origin)) throw Servers.Bad("That address answers, but it is not a Talaria server.")
                repo.signIn(address, password)
                autofill?.commit() // lets the phone's password manager offer to remember it
                password = ""
            } catch (e: Exception) { error = e.message ?: "Could not sign in." }
            finally { busy = false }
        }
    }

    Box(Modifier.fillMaxSize().safeDrawingPadding().imePadding(), contentAlignment = Alignment.Center) {
        Column(
            Modifier.widthIn(max = 420.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 24.dp, vertical = 24.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            TalariaMark(72.dp, Modifier.semantics { contentDescription = "Talaria" })
            Gap(20.dp)
            Text("Talaria", style = MaterialTheme.typography.displaySmall)
            Gap(4.dp)
            Text(
                if (notice != null) notice else "Sign in to your agent.",
                style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
            )
            Gap(28.dp)
            OutlinedTextField(
                value = address, onValueChange = { address = it; error = null },
                label = { Text("Server address") },
                placeholder = { Text("https://talaria.example.com") },
                singleLine = true, modifier = Modifier.fillMaxWidth().heightIn(min = 56.dp).semantics { contentType = ContentType.Username },
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Next),
                shape = MaterialTheme.shapes.medium,
            )
            Gap(12.dp)
            OutlinedTextField(
                value = password, onValueChange = { password = it; error = null },
                label = { Text("Password") },
                singleLine = true, modifier = Modifier.fillMaxWidth().heightIn(min = 56.dp).semantics { contentType = ContentType.Password },
                visualTransformation = if (show) VisualTransformation.None else PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, imeAction = ImeAction.Go),
                keyboardActions = KeyboardActions(onGo = { submit() }),
                trailingIcon = {
                    IconButton(onClick = { show = !show }) {
                        Icon(if (show) Ti.EyeOff else Ti.Eye, if (show) "Hide password" else "Show password")
                    }
                },
                shape = MaterialTheme.shapes.medium,
            )
            if (error != null) { Gap(12.dp); ErrorNote(error!!) }
            Gap(20.dp)
            Button(
                onClick = ::submit, enabled = !busy && address.isNotBlank() && password.isNotEmpty(),
                modifier = Modifier.fillMaxWidth().heightIn(min = 52.dp), shape = MaterialTheme.shapes.large,
                colors = ButtonDefaults.buttonColors(),
            ) {
                if (busy) CircularProgressIndicator(Modifier.padding(2.dp), strokeWidth = 2.dp, color = MaterialTheme.colorScheme.onPrimary)
                else Text("Sign in", style = MaterialTheme.typography.labelLarge)
            }
            Gap(20.dp)
            Text(
                "The address is where you run Talaria. A server on your own network can use http; anything on the internet needs https. Your password is sent once to sign in. Talaria does not keep it; your phone's password manager can fill it in and remember it for you.",
                style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center,
            )
            if (adding) { Gap(12.dp); TextButton(onClick = { repo.cancelAdd() }) { Text("Cancel") } }
        }
    }
}
