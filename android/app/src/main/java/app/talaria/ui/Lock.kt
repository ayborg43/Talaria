package app.talaria.ui

import android.os.Build
import androidx.appcompat.app.AppCompatActivity
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_WEAK
import androidx.biometric.BiometricManager.Authenticators.DEVICE_CREDENTIAL
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat

/** Fingerprint, face or the phone's own PIN. Strong biometrics plus the PIN where Android allows that mix. */
object Lock {
    private val authenticators get() = if (Build.VERSION.SDK_INT >= 30) BIOMETRIC_STRONG or DEVICE_CREDENTIAL else BIOMETRIC_WEAK or DEVICE_CREDENTIAL

    fun available(activity: AppCompatActivity) =
        BiometricManager.from(activity).canAuthenticate(authenticators) == BiometricManager.BIOMETRIC_SUCCESS

    fun prompt(activity: AppCompatActivity, title: String, subtitle: String, onOk: () -> Unit, onFail: (String) -> Unit) {
        val cb = object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) = onOk()
            override fun onAuthenticationError(errorCode: Int, errString: CharSequence) = onFail(errString.toString())
        }
        val info = BiometricPrompt.PromptInfo.Builder().setTitle(title).setSubtitle(subtitle).setAllowedAuthenticators(authenticators).build()
        BiometricPrompt(activity, ContextCompat.getMainExecutor(activity), cb).authenticate(info)
    }
}
